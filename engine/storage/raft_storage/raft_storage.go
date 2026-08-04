package raft_storage

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"

	badger "github.com/dgraph-io/badger/v4"
	"google.golang.org/protobuf/proto"

	"github.com/Aetherance/kv/engine/config"
	"github.com/Aetherance/kv/engine/raftruntime"
	"github.com/Aetherance/kv/engine/storage"
	engine_util "github.com/Aetherance/kv/engine/util"
	"github.com/Aetherance/kv/log"
	"github.com/Aetherance/kv/proto/pkg/raft_cmdpb"
	rspb "github.com/Aetherance/kv/proto/pkg/raft_serverpb"
	"github.com/Aetherance/kv/proto/pkg/raftpb"
	"github.com/Aetherance/kv/raft"
)

var (
	runtimeNamespace = []byte("\xfftaluskv/raft/")
	identityKey      = []byte("\xfftaluskv/node-id")
	proposalMagic    = [4]byte{'T', 'K', 'V', 1}
)

var errLeadershipLost = errors.New("raft storage: leadership lost before proposal was applied")

type requestID struct {
	nodeID   uint64
	sequence uint64
}

type proposalResult struct {
	response *raft_cmdpb.RaftCmdResponse
	err      error
}

// RaftStorage is the application-facing adapter around the generic Raft
// runtime. Fixed node addresses, request correlation, and gRPC envelopes live
// here so the runtime remains unaware of stores, regions, and groups.
type RaftStorage struct {
	rspb.UnimplementedRaftServiceServer

	config *config.Config
	db     *badger.DB
	engine *raftruntime.BadgerEngine

	runtime   *raftruntime.Runtime
	runner    *raftruntime.Runner
	transport *ServerTransport
	cancel    context.CancelFunc
	runDone   chan error

	sequence  atomic.Uint64
	pendingMu sync.Mutex
	pending   map[uint64]chan proposalResult

	lifecycleMu sync.Mutex
	started     bool
	stopped     bool
}

func NewRaftStorage(conf *config.Config) *RaftStorage {
	return &RaftStorage{
		config:  conf,
		pending: make(map[uint64]chan proposalResult),
	}
}

func (rs *RaftStorage) Write(batch []storage.Modify) error {
	if len(batch) == 0 {
		return nil
	}
	requests := make([]*raft_cmdpb.Request, 0, len(batch))
	for _, modification := range batch {
		switch data := modification.Data.(type) {
		case storage.Put:
			requests = append(requests, &raft_cmdpb.Request{
				CmdType: raft_cmdpb.CmdType_Put,
				Put:     &raft_cmdpb.PutRequest{Cf: data.Cf, Key: data.Key, Value: data.Val},
			})
		case storage.Delete:
			requests = append(requests, &raft_cmdpb.Request{
				CmdType: raft_cmdpb.CmdType_Delete,
				Delete:  &raft_cmdpb.DeleteRequest{Cf: data.Cf, Key: data.Key},
			})
		default:
			return fmt.Errorf("raft storage: unsupported modification %T", modification.Data)
		}
	}

	response, err := rs.propose(&raft_cmdpb.RaftCmdRequest{Requests: requests})
	if err != nil {
		return err
	}
	if len(response.Responses) != len(requests) {
		return fmt.Errorf("raft storage: response count %d does not match request count %d", len(response.Responses), len(requests))
	}
	return nil
}

func (rs *RaftStorage) Reader() (storage.StorageReader, error) {
	request := &raft_cmdpb.RaftCmdRequest{Requests: []*raft_cmdpb.Request{{
		CmdType: raft_cmdpb.CmdType_Snap,
		Snap:    &raft_cmdpb.SnapRequest{},
	}}}
	if _, err := rs.propose(request); err != nil {
		return nil, err
	}
	return &badgerReader{txn: rs.db.NewTransaction(false)}, nil
}

func (rs *RaftStorage) Start() error {
	rs.lifecycleMu.Lock()
	defer rs.lifecycleMu.Unlock()
	if rs.started {
		return nil
	}
	if rs.stopped {
		return errors.New("raft storage: cannot restart a stopped instance")
	}
	if err := validateConfig(rs.config); err != nil {
		return err
	}

	db, err := openDB(filepath.Join(rs.config.DBPath, "db"))
	if err != nil {
		return err
	}
	rs.db = db
	cleanup := func() {
		_ = db.Close()
		rs.db = nil
	}
	if err := checkIdentity(db, rs.config.StoreID); err != nil {
		cleanup()
		return err
	}

	peers := sortedPeerIDs(rs.config.Peers)
	engine, err := raftruntime.OpenBadgerEngine(db, runtimeNamespace, peers, raftruntime.Snapshotter{
		Capture: captureKVSnapshot,
		Restore: restoreKVSnapshot,
	})
	if err != nil {
		cleanup()
		return err
	}
	rs.engine = engine

	rawNode, err := raft.NewRawNode(&raft.Config{
		ID:            rs.config.StoreID,
		ElectionTick:  rs.config.RaftElectionTimeoutTicks,
		HeartbeatTick: rs.config.RaftHeartbeatTicks,
		Storage:       engine,
		Applied:       engine.AppliedIndex(),
	})
	if err != nil {
		cleanup()
		return err
	}

	rs.transport = NewServerTransport(rs.config)
	rs.runtime = raftruntime.New(rawNode, engine, rs.applyCommand, raftruntime.Config{
		CompactThreshold: rs.config.RaftLogGcCountLimit,
		Send:             rs.sendMessages,
		Applied:          rs.onApplied,
		StateChanged:     rs.onStateChanged,
	})
	rs.runner = raftruntime.NewRunner(rs.runtime, rs.config.RaftBaseTickInterval)
	runCtx, cancel := context.WithCancel(context.Background())
	rs.cancel = cancel
	rs.runDone = make(chan error, 1)
	go func() {
		err := rs.runner.Run(runCtx)
		rs.failPending(raftruntime.ErrStopped)
		rs.runDone <- err
	}()
	rs.started = true

	if len(peers) == 1 {
		if err := rs.runner.Campaign(context.Background()); err != nil {
			cancel()
			<-rs.runDone
			rs.transport.Stop()
			cleanup()
			rs.started = false
			return err
		}
	}
	return nil
}

func (rs *RaftStorage) Stop() error {
	rs.lifecycleMu.Lock()
	if rs.stopped {
		rs.lifecycleMu.Unlock()
		return nil
	}
	rs.stopped = true
	started := rs.started
	cancel := rs.cancel
	runDone := rs.runDone
	transport := rs.transport
	db := rs.db
	rs.lifecycleMu.Unlock()

	if !started {
		return nil
	}
	if cancel != nil {
		cancel()
	}
	// Cancel network streams before waiting for the runner so a blocked send
	// cannot hold shutdown indefinitely.
	if transport != nil {
		transport.Stop()
	}
	if runDone != nil {
		<-runDone
	}
	rs.failPending(raftruntime.ErrStopped)
	if db != nil {
		return db.Close()
	}
	return nil
}

// Raft adapts the existing gRPC envelope to the raw messages consumed by the
// runtime. The envelope is intentionally outside the generic runtime.
func (rs *RaftStorage) Raft(stream rspb.RaftService_RaftServer) error {
	for {
		envelope, err := stream.Recv()
		if err == io.EOF {
			return stream.SendAndClose(&rspb.Done{})
		}
		if err != nil {
			return err
		}
		message := envelope.GetMessage()
		if message == nil {
			continue
		}
		if message.To != 0 && message.To != rs.config.StoreID {
			continue
		}
		if err := rs.runner.Step(stream.Context(), message); err != nil {
			return err
		}
	}
}

func (rs *RaftStorage) propose(request *raft_cmdpb.RaftCmdRequest) (*raft_cmdpb.RaftCmdResponse, error) {
	payload, err := request.Marshal()
	if err != nil {
		return nil, err
	}
	sequence := rs.sequence.Add(1)
	data := encodeProposal(requestID{nodeID: rs.config.StoreID, sequence: sequence}, payload)
	resultCh := make(chan proposalResult, 1)

	rs.pendingMu.Lock()
	rs.pending[sequence] = resultCh
	rs.pendingMu.Unlock()

	if err := rs.runner.Propose(context.Background(), data); err != nil {
		rs.removePending(sequence)
		return nil, err
	}
	result := <-resultCh
	return result.response, result.err
}

func (rs *RaftStorage) applyCommand(txn *badger.Txn, _ uint64, data []byte) ([]byte, error) {
	_, payload, err := decodeProposal(data)
	if err != nil {
		return nil, err
	}
	request := new(raft_cmdpb.RaftCmdRequest)
	if err := request.Unmarshal(payload); err != nil {
		return nil, err
	}

	response := &raft_cmdpb.RaftCmdResponse{
		Header:    &raft_cmdpb.RaftResponseHeader{},
		Responses: make([]*raft_cmdpb.Response, 0, len(request.Requests)),
	}
	for _, command := range request.Requests {
		switch command.CmdType {
		case raft_cmdpb.CmdType_Put:
			if command.Put == nil {
				return nil, errors.New("raft storage: put command has no payload")
			}
			if err := txn.Set(engine_util.KeyWithCF(command.Put.Cf, command.Put.Key), command.Put.Value); err != nil {
				return nil, err
			}
		case raft_cmdpb.CmdType_Delete:
			if command.Delete == nil {
				return nil, errors.New("raft storage: delete command has no payload")
			}
			if err := txn.Delete(engine_util.KeyWithCF(command.Delete.Cf, command.Delete.Key)); err != nil {
				return nil, err
			}
		case raft_cmdpb.CmdType_Snap:
			// A committed no-op command is the deliberately simple read barrier.
		default:
			return nil, fmt.Errorf("raft storage: unsupported command %s", command.CmdType)
		}
		response.Responses = append(response.Responses, &raft_cmdpb.Response{CmdType: command.CmdType})
	}
	return proto.Marshal(response)
}

func (rs *RaftStorage) onApplied(entries []raftruntime.Applied) {
	for _, entry := range entries {
		if entry.Type != raftpb.EntryType_EntryNormal {
			continue
		}
		id, _, err := decodeProposal(entry.Data)
		if err != nil || id.nodeID != rs.config.StoreID {
			continue
		}
		response := new(raft_cmdpb.RaftCmdResponse)
		if err == nil {
			err = proto.Unmarshal(entry.Result, response)
		}
		rs.completePending(id.sequence, proposalResult{response: response, err: err})
	}
}

func (rs *RaftStorage) onStateChanged(state raft.SoftState) {
	if state.RaftState != raft.StateLeader {
		rs.failPending(errLeadershipLost)
	}
}

func (rs *RaftStorage) sendMessages(messages []*raftpb.Message) {
	for _, message := range messages {
		if err := rs.transport.Send(message); err != nil {
			log.Errorf("send raft message %s from %d to %d: %v", message.MsgType, message.From, message.To, err)
		}
	}
}

func (rs *RaftStorage) completePending(sequence uint64, result proposalResult) {
	rs.pendingMu.Lock()
	resultCh := rs.pending[sequence]
	delete(rs.pending, sequence)
	rs.pendingMu.Unlock()
	if resultCh != nil {
		resultCh <- result
	}
}

func (rs *RaftStorage) removePending(sequence uint64) {
	rs.pendingMu.Lock()
	delete(rs.pending, sequence)
	rs.pendingMu.Unlock()
}

func (rs *RaftStorage) failPending(err error) {
	rs.pendingMu.Lock()
	pending := rs.pending
	rs.pending = make(map[uint64]chan proposalResult)
	rs.pendingMu.Unlock()
	for _, resultCh := range pending {
		resultCh <- proposalResult{err: err}
	}
}

func encodeProposal(id requestID, payload []byte) []byte {
	data := make([]byte, 20+len(payload))
	copy(data[:4], proposalMagic[:])
	binary.BigEndian.PutUint64(data[4:12], id.nodeID)
	binary.BigEndian.PutUint64(data[12:20], id.sequence)
	copy(data[20:], payload)
	return data
}

func decodeProposal(data []byte) (requestID, []byte, error) {
	if len(data) < 20 || string(data[:4]) != string(proposalMagic[:]) {
		return requestID{}, nil, errors.New("raft storage: invalid proposal envelope")
	}
	return requestID{
		nodeID:   binary.BigEndian.Uint64(data[4:12]),
		sequence: binary.BigEndian.Uint64(data[12:20]),
	}, data[20:], nil
}

func captureKVSnapshot(txn *badger.Txn) ([]byte, error) {
	snapshot := new(rspb.RaftSnapshotData)
	for _, cf := range engine_util.CFs {
		prefix := []byte(cf + "_")
		iterator := txn.NewIterator(badger.DefaultIteratorOptions)
		for iterator.Seek(prefix); iterator.ValidForPrefix(prefix); iterator.Next() {
			item := iterator.Item()
			value, err := item.ValueCopy(nil)
			if err != nil {
				iterator.Close()
				return nil, err
			}
			snapshot.Data = append(snapshot.Data, &rspb.KeyValue{
				Key:   item.KeyCopy(nil),
				Value: value,
			})
		}
		iterator.Close()
	}
	return snapshot.Marshal()
}

func restoreKVSnapshot(txn *badger.Txn, data []byte) error {
	snapshot := new(rspb.RaftSnapshotData)
	if err := snapshot.Unmarshal(data); err != nil {
		return err
	}
	for _, cf := range engine_util.CFs {
		prefix := []byte(cf + "_")
		iterator := txn.NewIterator(badger.DefaultIteratorOptions)
		for iterator.Seek(prefix); iterator.ValidForPrefix(prefix); iterator.Next() {
			if err := txn.Delete(iterator.Item().KeyCopy(nil)); err != nil {
				iterator.Close()
				return err
			}
		}
		iterator.Close()
	}
	for _, pair := range snapshot.Data {
		if err := txn.Set(pair.Key, pair.Value); err != nil {
			return err
		}
	}
	return nil
}

func validateConfig(config *config.Config) error {
	if config == nil {
		return errors.New("raft storage: nil config")
	}
	if config.StoreID == 0 {
		return errors.New("raft storage: store ID must be non-zero")
	}
	if _, ok := config.Peers[config.StoreID]; !ok {
		return fmt.Errorf("raft storage: store %d is absent from peers", config.StoreID)
	}
	if config.RaftBaseTickInterval <= 0 || config.RaftHeartbeatTicks <= 0 || config.RaftElectionTimeoutTicks <= config.RaftHeartbeatTicks {
		return errors.New("raft storage: invalid raft timing configuration")
	}
	return nil
}

func sortedPeerIDs(peers map[uint64]string) []uint64 {
	ids := make([]uint64, 0, len(peers))
	for id := range peers {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func openDB(path string) (*badger.DB, error) {
	options := badger.DefaultOptions(path)
	options.SyncWrites = true
	return badger.Open(options)
}

func checkIdentity(db *badger.DB, nodeID uint64) error {
	return db.Update(func(txn *badger.Txn) error {
		item, err := txn.Get(identityKey)
		if err == badger.ErrKeyNotFound {
			var value [8]byte
			binary.BigEndian.PutUint64(value[:], nodeID)
			return txn.Set(identityKey, value[:])
		}
		if err != nil {
			return err
		}
		value, err := item.ValueCopy(nil)
		if err != nil {
			return err
		}
		if len(value) != 8 || binary.BigEndian.Uint64(value) != nodeID {
			return fmt.Errorf("raft storage: persisted node identity does not match node %d", nodeID)
		}
		return nil
	})
}

type badgerReader struct {
	txn *badger.Txn
}

func (r *badgerReader) GetCF(cf string, key []byte) ([]byte, error) {
	value, err := engine_util.GetCFFromTxn(r.txn, cf, key)
	if err == badger.ErrKeyNotFound {
		return nil, nil
	}
	return value, err
}

func (r *badgerReader) IterCF(cf string) engine_util.Iterator {
	return engine_util.NewCFIterator(cf, r.txn)
}

func (r *badgerReader) Close() {
	r.txn.Discard()
}
