package raft_storage

import (
	"fmt"
	"os"
	"path/filepath"

	badger "github.com/dgraph-io/badger/v4"

	"github.com/Aetherance/kv/engine/config"
	"github.com/Aetherance/kv/engine/raftstore"
	"github.com/Aetherance/kv/engine/raftstore/message"
	"github.com/Aetherance/kv/engine/storage"
	engine_util "github.com/Aetherance/kv/engine/util"
	"github.com/Aetherance/kv/proto/pkg/errorpb"
	"github.com/Aetherance/kv/proto/pkg/metapb"
	"github.com/Aetherance/kv/proto/pkg/raft_cmdpb"
	rspb "github.com/Aetherance/kv/proto/pkg/raft_serverpb"
)

// RaftStorage is a storage.Storage backed by a raftstore. Reads and writes go
// through Raft, so they are consistent across the static cluster.
type RaftStorage struct {
	rspb.UnimplementedRaftServiceServer

	engines *engine_util.Engines
	config  *config.Config

	node       *raftstore.Node
	raftRouter *raftstore.RaftstoreRouter
	raftSystem *raftstore.Raftstore
	trans      *ServerTransport
}

type RegionError struct {
	RequestErr *errorpb.Error
}

func (re *RegionError) Error() string {
	return re.RequestErr.String()
}

func NewRaftStorage(conf *config.Config) *RaftStorage {
	dbPath := conf.DBPath
	kvPath := filepath.Join(dbPath, "kv")
	raftPath := filepath.Join(dbPath, "raft")

	os.MkdirAll(kvPath, os.ModePerm)
	os.MkdirAll(raftPath, os.ModePerm)

	raftDB := engine_util.CreateDB(raftPath, true)
	kvDB := engine_util.CreateDB(kvPath, false)
	engines := engine_util.NewEngines(kvDB, raftDB, kvPath, raftPath)

	return &RaftStorage{engines: engines, config: conf}
}

func (rs *RaftStorage) checkResponse(resp *raft_cmdpb.RaftCmdResponse, reqCount int) error {
	if resp.Header.Error != nil {
		return &RegionError{RequestErr: resp.Header.Error}
	}
	if len(resp.Responses) != reqCount {
		return fmt.Errorf("responses count %d is not equal to requests count %d", len(resp.Responses), reqCount)
	}
	return nil
}

// header builds the raft command header for the fixed region using this store's
// peer identity and the fixed region epoch. There is no scheduler, so the
// client request carries no region context.
func (rs *RaftStorage) header() *raft_cmdpb.RaftRequestHeader {
	return &raft_cmdpb.RaftRequestHeader{
		RegionId: raftstore.RegionID,
		Peer: &metapb.Peer{
			Id:      rs.config.StoreID,
			StoreId: rs.config.StoreID,
		},
		RegionEpoch: &metapb.RegionEpoch{
			Version: raftstore.InitEpochVer,
			ConfVer: raftstore.InitEpochConfVer,
		},
	}
}

func (rs *RaftStorage) Write(batch []storage.Modify) error {
	var reqs []*raft_cmdpb.Request
	for _, m := range batch {
		switch data := m.Data.(type) {
		case storage.Put:
			reqs = append(reqs, &raft_cmdpb.Request{
				CmdType: raft_cmdpb.CmdType_Put,
				Put:     &raft_cmdpb.PutRequest{Cf: data.Cf, Key: data.Key, Value: data.Val},
			})
		case storage.Delete:
			reqs = append(reqs, &raft_cmdpb.Request{
				CmdType: raft_cmdpb.CmdType_Delete,
				Delete:  &raft_cmdpb.DeleteRequest{Cf: data.Cf, Key: data.Key},
			})
		}
	}

	request := &raft_cmdpb.RaftCmdRequest{Header: rs.header(), Requests: reqs}
	cb := message.NewCallback()
	if err := rs.raftRouter.SendRaftCommand(request, cb); err != nil {
		return err
	}
	return rs.checkResponse(cb.WaitResp(), len(reqs))
}

func (rs *RaftStorage) Reader() (storage.StorageReader, error) {
	request := &raft_cmdpb.RaftCmdRequest{
		Header: rs.header(),
		Requests: []*raft_cmdpb.Request{{
			CmdType: raft_cmdpb.CmdType_Snap,
			Snap:    &raft_cmdpb.SnapRequest{},
		}},
	}
	cb := message.NewCallback()
	if err := rs.raftRouter.SendRaftCommand(request, cb); err != nil {
		return nil, err
	}
	resp := cb.WaitResp()
	if err := rs.checkResponse(resp, 1); err != nil {
		if cb.Txn != nil {
			cb.Txn.Discard()
		}
		return nil, err
	}
	if cb.Txn == nil {
		return nil, fmt.Errorf("could not get region snapshot")
	}
	return newRegionReader(cb.Txn), nil
}

// Raft is the gRPC handler for inbound raft message streams from other stores.
func (rs *RaftStorage) Raft(stream rspb.RaftService_RaftServer) error {
	for {
		msg, err := stream.Recv()
		if err != nil {
			return err
		}
		_ = rs.raftRouter.SendRaftMessage(msg)
	}
}

func (rs *RaftStorage) Start() error {
	cfg := rs.config
	rs.raftRouter, rs.raftSystem = raftstore.CreateRaftstore(cfg)
	rs.trans = NewServerTransport(cfg)
	rs.node = raftstore.NewNode(rs.raftSystem, cfg)
	return rs.node.Start(rs.engines, rs.trans)
}

func (rs *RaftStorage) Stop() error {
	rs.node.Stop()
	rs.trans.Stop()
	if err := rs.engines.Raft.Close(); err != nil {
		return err
	}
	return rs.engines.Kv.Close()
}

// regionReader reads the committed kv snapshot returned by a Snap raft command.
// The single region spans the whole keyspace, so no key-range bounds are needed.
type regionReader struct {
	txn *badger.Txn
}

func newRegionReader(txn *badger.Txn) *regionReader {
	return &regionReader{txn: txn}
}

func (r *regionReader) GetCF(cf string, key []byte) ([]byte, error) {
	val, err := engine_util.GetCFFromTxn(r.txn, cf, key)
	if err == badger.ErrKeyNotFound {
		return nil, nil
	}
	return val, err
}

func (r *regionReader) IterCF(cf string) engine_util.Iterator {
	return engine_util.NewCFIterator(cf, r.txn)
}

func (r *regionReader) Close() {
	r.txn.Discard()
}
