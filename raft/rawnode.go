package raft

import (
	"errors"

	pb "github.com/Aetherance/kv/proto/pkg/raftpb"
	"google.golang.org/protobuf/proto"
)

// ErrStepLocalMsg is returned when try to step a local raft message
var ErrStepLocalMsg = errors.New("raft: cannot step raft local message")

// ErrStepPeerNotFound is returned when try to step a response message
// but there is no peer found in raft.Prs for that node.
var ErrStepPeerNotFound = errors.New("raft: cannot step as peer not found")

// SoftState provides state that is volatile and does not need to be persisted to the WAL.
type SoftState struct {
	Lead      uint64
	RaftState StateType
}

// ReadState provides the commit index that is safe to use for a linearizable
// read and the opaque context supplied with the corresponding ReadIndex call.
type ReadState struct {
	Index      uint64
	RequestCtx []byte
}

// Ready encapsulates the entries and messages that are ready to read,
// be saved to stable storage, committed or sent to other peers.
// All fields in Ready are read-only.
type Ready struct {
	// The current volatile state of a Node.
	// SoftState will be nil if there is no update.
	// It is not required to consume or store SoftState.
	*SoftState

	// The current state of a Node to be saved to stable storage BEFORE
	// Messages are sent.
	// HardState will be nil if there is no update.
	*pb.HardState

	// Entries specifies entries to be saved to stable storage BEFORE
	// Messages are sent.
	Entries []*pb.Entry

	// Snapshot specifies the snapshot to be saved to stable storage.
	Snapshot *pb.Snapshot

	// CommittedEntries specifies entries to be committed to a
	// store/state-machine. These have previously been committed to stable
	// store.
	CommittedEntries []*pb.Entry

	// Messages specifies outbound messages to be sent AFTER Entries are
	// committed to stable storage.
	// If it contains a MessageType_MsgSnapshot message, the application MUST report back to raft
	// when the snapshot has been received or has failed by calling ReportSnapshot.
	Messages []*pb.Message

	// ReadStates specifies completed linearizable read-index requests.
	ReadStates []ReadState
}

// RawNode is a wrapper of Raft.
type RawNode struct {
	Raft *Raft

	PrevStates
	// Your Data Here (2A).
}

type PrevStates struct {
	PrevSoftState *SoftState
	PrevHardState *pb.HardState
}

// NewRawNode returns a new RawNode given configuration and a list of raft peers.
func NewRawNode(config *Config) (*RawNode, error) {
	raft := newRaft(config)

	return &RawNode{
		Raft: raft,
		PrevStates: PrevStates{
			PrevSoftState: &SoftState{Lead: raft.Lead, RaftState: raft.State},
			PrevHardState: &pb.HardState{Term: raft.Term, Vote: raft.Vote, Commit: raft.RaftLog.committed},
		},
	}, nil
}

// Tick advances the internal logical clock by a single tick.
func (rn *RawNode) Tick() {
	rn.Raft.tick()
}

// Campaign causes this RawNode to start a pre-election.
func (rn *RawNode) Campaign() error {
	return rn.Raft.Step(&pb.Message{
		MsgType: pb.MessageType_MsgHup,
	})
}

// Propose proposes data be appended to the raft log.
func (rn *RawNode) Propose(data []byte) error {
	ent := pb.Entry{Data: data}
	return rn.Raft.Step(&pb.Message{
		MsgType: pb.MessageType_MsgPropose,
		From:    rn.Raft.id,
		Entries: []*pb.Entry{&ent}})
}

// ReadIndex requests a linearizable read index using an opaque, caller-unique
// context. The result is returned asynchronously through Ready.ReadStates.
func (rn *RawNode) ReadIndex(requestCtx []byte) error {
	return rn.Raft.Step(&pb.Message{
		MsgType: pb.MessageType_MsgReadIndex,
		From:    rn.Raft.id,
		To:      rn.Raft.id,
		Term:    rn.Raft.Term,
		Context: append([]byte(nil), requestCtx...),
	})
}

// TODO: configuration changes are not supported until Raft membership updates
// and dynamic peer transport management are implemented.
// ProposeConfChange proposes a config change.
func (rn *RawNode) ProposeConfChange(cc *pb.ConfChange) error {
	data, err := proto.Marshal(cc)
	if err != nil {
		return err
	}
	ent := pb.Entry{EntryType: pb.EntryType_EntryConfChange, Data: data}
	return rn.Raft.Step(&pb.Message{
		MsgType: pb.MessageType_MsgPropose,
		Entries: []*pb.Entry{&ent},
	})
}

// TODO: configuration changes are not supported until Raft membership updates
// and dynamic peer transport management are implemented.
// ApplyConfChange applies a config change to the local node.
func (rn *RawNode) ApplyConfChange(cc *pb.ConfChange) *pb.ConfState {
	if cc.NodeId == None {
		return &pb.ConfState{Nodes: nodes(rn.Raft)}
	}
	switch cc.ChangeType {
	case pb.ConfChangeType_AddNode:
		rn.Raft.addNode(cc.NodeId)
	case pb.ConfChangeType_RemoveNode:
		rn.Raft.removeNode(cc.NodeId)
	default:
		panic("unexpected conf type")
	}
	return &pb.ConfState{Nodes: nodes(rn.Raft)}
}

// Step advances the state machine using the given message.
func (rn *RawNode) Step(m *pb.Message) error {
	// ignore unexpected local messages receiving over network
	if IsLocalMsg(m.MsgType) {
		return ErrStepLocalMsg
	}
	if pr := rn.Raft.Prs[m.From]; pr != nil || !IsResponseMsg(m.MsgType) {
		return rn.Raft.Step(m)
	}
	return ErrStepPeerNotFound
}

func (rn *RawNode) softState() *SoftState {
	return &SoftState{
		Lead:      rn.Raft.Lead,
		RaftState: rn.Raft.State,
	}
}

func (rn *RawNode) hardState() *pb.HardState {
	return &pb.HardState{
		Term:   rn.Raft.Term,
		Vote:   rn.Raft.Vote,
		Commit: rn.Raft.RaftLog.committed,
	}
}

// Ready returns the current point-in-time state of this RawNode.
func (rn *RawNode) Ready() Ready {
	currentSoftState := rn.softState()
	softState := currentSoftState

	if rn.PrevSoftState != nil && *softState == *rn.PrevSoftState {
		softState = nil
	}

	currentHardState := rn.hardState()

	var hardState *pb.HardState
	if rn.PrevHardState == nil || !isHardStateEqual(currentHardState, rn.PrevHardState) {
		hardState = currentHardState
	}

	msg := rn.Raft.msgs
	if len(rn.Raft.msgs) <= 0 {
		msg = nil
	}

	rd := Ready{
		SoftState:        softState,
		HardState:        hardState,
		Entries:          rn.Raft.RaftLog.unstableEntries(),
		CommittedEntries: rn.Raft.RaftLog.nextEnts(),
		Messages:         msg,
		Snapshot:         rn.Raft.RaftLog.pendingSnapshot,
		ReadStates:       rn.Raft.readStates,
	}

	rn.PrevHardState = currentHardState
	rn.PrevSoftState = currentSoftState

	rn.Raft.msgs = nil
	rn.Raft.readStates = nil
	return rd
}

// HasReady called when RawNode user need to check if any Ready pending.
func (rn *RawNode) HasReady() bool {
	currentSoftState := rn.softState()
	if rn.PrevSoftState == nil || *currentSoftState != *rn.PrevSoftState {
		return true
	}
	currentHardState := rn.hardState()
	if rn.PrevHardState == nil || !isHardStateEqual(currentHardState, rn.PrevHardState) {
		return true
	}
	return len(rn.Raft.msgs) > 0 ||
		len(rn.Raft.readStates) > 0 ||
		len(rn.Raft.RaftLog.unstableEntries()) > 0 ||
		len(rn.Raft.RaftLog.nextEnts()) > 0 ||
		rn.Raft.RaftLog.pendingSnapshot != nil
}

// Advance notifies the RawNode that the application has applied and saved progress in the
// last Ready results.
func (rn *RawNode) Advance(rd *Ready) {
	if len(rd.CommittedEntries) > 0 {
		rn.Raft.RaftLog.applied = rd.CommittedEntries[len(rd.CommittedEntries)-1].Index
	}

	if len(rd.Entries) > 0 {
		rn.Raft.RaftLog.stabled = rd.Entries[len(rd.Entries)-1].Index
	}

	if !IsEmptySnap(rd.Snapshot) {
		rn.Raft.RaftLog.pendingSnapshot = nil
	}
	rn.Raft.RaftLog.maybeCompact()
}

// GetProgress return the Progress of this node and its peers, if this
// node is leader.
func (rn *RawNode) GetProgress() map[uint64]Progress {
	prs := make(map[uint64]Progress)
	if rn.Raft.State == StateLeader {
		for id, p := range rn.Raft.Prs {
			prs[id] = *p
		}
	}
	return prs
}

// TransferLeader tries to transfer leadership to the given transferee.
func (rn *RawNode) TransferLeader(transferee uint64) {
	_ = rn.Raft.Step(&pb.Message{MsgType: pb.MessageType_MsgTransferLeader, From: transferee})
}
