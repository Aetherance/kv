package raft

import (
	"errors"
	"math/rand"

	pb "github.com/Aetherance/kv/proto/pkg/raftpb"
)

// None is a placeholder node ID used when there is no leader.
const None uint64 = 0

// StateType represents the role of a node in a cluster.
type StateType uint64

const (
	StateFollower StateType = iota
	StateCandidate
	StateLeader
)

var stmap = [...]string{
	"StateFollower",
	"StateCandidate",
	"StateLeader",
}

func (st StateType) String() string {
	return stmap[uint64(st)]
}

// ErrProposalDropped is returned when the proposal is ignored by some cases,
// so that the proposer can be notified and fail fast.
var ErrProposalDropped = errors.New("raft proposal dropped")

// Config contains the parameters to start a raft.
type Config struct {
	// ID is the identity of the local raft. ID cannot be 0.
	ID uint64

	// peers contains the IDs of all nodes (including self) in the raft cluster. It
	// should only be set when starting a new raft cluster. Restarting raft from
	// previous configuration will panic if peers is set. peer is private and only
	// used for testing right now.
	peers []uint64

	// ElectionTick is the number of Node.Tick invocations that must pass between
	// elections. That is, if a follower does not receive any message from the
	// leader of current term before ElectionTick has elapsed, it will become
	// candidate and start an election. ElectionTick must be greater than
	// HeartbeatTick. We suggest ElectionTick = 10 * HeartbeatTick to avoid
	// unnecessary leader switching.
	ElectionTick int
	// HeartbeatTick is the number of Node.Tick invocations that must pass between
	// heartbeats. That is, a leader sends heartbeat messages to maintain its
	// leadership every HeartbeatTick ticks.
	HeartbeatTick int

	// Storage is the storage for raft. raft generates entries and states to be
	// stored in storage. raft reads the persisted entries and states out of
	// Storage when it needs. raft reads out the previous state and configuration
	// out of storage when restarting.
	Storage Storage
	// Applied is the last applied index. It should only be set when restarting
	// raft. raft will not return entries to the application smaller or equal to
	// Applied. If Applied is unset when restarting, raft might return previous
	// applied entries. This is a very application dependent configuration.
	Applied uint64
}

func (c *Config) validate() error {
	if c.ID == None {
		return errors.New("cannot use none as id")
	}

	if c.HeartbeatTick <= 0 {
		return errors.New("heartbeat tick must be greater than 0")
	}

	if c.ElectionTick <= c.HeartbeatTick {
		return errors.New("election tick must be greater than heartbeat tick")
	}

	if c.Storage == nil {
		return errors.New("storage cannot be nil")
	}

	return nil
}

// Progress represents a follower’s progress in the view of the leader. Leader maintains
// progresses of all followers, and sends entries to the follower based on its progress.
type Progress struct {
	Match, Next uint64
}

type Raft struct {
	id uint64

	Term uint64
	Vote uint64

	// the log
	RaftLog *RaftLog

	// log replication progress of each peers
	Prs map[uint64]*Progress

	// this peer's role
	State StateType

	// votes records
	votes map[uint64]bool

	// msgs need to send
	msgs []*pb.Message

	// the leader id
	Lead uint64

	// heartbeat interval, should send
	heartbeatTimeout int
	// baseline of election interval
	electionTimeout int
	// true election timeout
	randomElectionTimeout int
	// number of ticks since it reached last heartbeatTimeout.
	// only leader keeps heartbeatElapsed.
	heartbeatElapsed int
	// Ticks since it reached last electionTimeout when it is leader or candidate.
	// Number of ticks since it reached last electionTimeout or received a
	// valid message from current leader when it is a follower.
	electionElapsed int

	// leadTransferee is id of the leader transfer target when its value is not zero.
	// Follow the procedure defined in section 3.10 of Raft phd thesis.
	// (https://web.stanford.edu/~ouster/cgi-bin/papers/OngaroPhD.pdf)
	// (Used in 3A leader transfer)
	leadTransferee uint64

	// Only one conf change may be pending (in the log, but not yet
	// applied) at a time. This is enforced via PendingConfIndex, which
	// is set to a value >= the log index of the latest pending
	// configuration change (if any). Config changes are only allowed to
	// be proposed if the leader's applied index is greater than this
	// value.
	// (Used in 3A conf change)
	PendingConfIndex uint64
}

// newRaft return a raft peer with the given config
func newRaft(c *Config) *Raft {
	if err := c.validate(); err != nil {
		panic(err.Error())
	}

	prs := make(map[uint64]*Progress)
	for _, id := range c.peers {
		prs[id] = &Progress{}
	}

	randElectionTimeout := c.ElectionTick + rand.Intn(c.ElectionTick)
	hs, cs, _ := c.Storage.InitialState()

	if len(prs) == 0 {
		for _, id := range cs.Nodes {
			prs[id] = &Progress{}
		}
	}

	raftLog := newLog(c.Storage)
	if c.Applied > 0 {
		if c.Applied < raftLog.entries[0].Index || c.Applied > raftLog.committed {
			panic("applied index is outside the committed raft log")
		}
		raftLog.applied = c.Applied
	}

	return &Raft{
		id:                    c.ID,
		RaftLog:               raftLog,
		Prs:                   prs,
		State:                 StateFollower,
		Term:                  hs.Term,
		Vote:                  hs.Vote,
		Lead:                  None,
		votes:                 make(map[uint64]bool),
		msgs:                  make([]*pb.Message, 0),
		heartbeatTimeout:      c.HeartbeatTick,
		electionTimeout:       c.ElectionTick,
		randomElectionTimeout: randElectionTimeout,
	}
}

// sendAppend sends an append RPC with new entries (if any) and the
// current commit index to the given peer. Returns true if a message was sent.
func (r *Raft) sendAppend(to uint64) bool {
	pr := r.Prs[to]
	prevLogIndex := pr.Next - 1
	prevLogTerm, err := r.RaftLog.Term(prevLogIndex)
	if err != nil {
		if err == ErrCompacted {
			r.sendSnapshot(to)
		}
		return false
	}
	offset := r.RaftLog.entries[0].Index
	ents := r.RaftLog.entries[pr.Next-offset:]

	r.msgs = append(r.msgs, &pb.Message{
		MsgType: pb.MessageType_MsgAppend,
		From:    r.id,
		To:      to,
		Term:    r.Term,
		Index:   prevLogIndex,
		LogTerm: prevLogTerm,
		Entries: ents,
		Commit:  r.RaftLog.committed,
	})
	return true
}

// sendHeartbeat sends a heartbeat RPC to the given peer.
func (r *Raft) sendHeartbeat(to uint64) {
	r.msgs = append(r.msgs, &pb.Message{
		From:    r.id,
		To:      to,
		Term:    r.Term,
		MsgType: pb.MessageType_MsgHeartbeat,
		Commit:  r.RaftLog.committed,
	})
}

// tick advances the internal logical clock by a single tick.
func (r *Raft) tick() {
	switch r.State {
	case StateFollower, StateCandidate:
		r.electionElapsed++
		if r.electionElapsed >= r.randomElectionTimeout {
			r.electionElapsed = 0
			r.campaign()
		}
	case StateLeader:
		r.heartbeatElapsed++
		if r.heartbeatElapsed >= r.heartbeatTimeout {
			for id := range r.Prs {
				if id != r.id {
					r.sendHeartbeat(id)
				}
			}
		}
	}
}

func (r *Raft) campaign() {
	r.becomeCandidate()
	if len(r.Prs) == 1 {
		r.becomeLeader()
		return
	}
	logIdx := r.RaftLog.LastIndex()
	logTerm, _ := r.RaftLog.Term(logIdx)

	for prId, _ := range r.Prs {
		if prId != r.id {
			r.msgs = append(r.msgs, &pb.Message{
				From:    r.id,
				To:      prId,
				Term:    r.Term,
				MsgType: pb.MessageType_MsgRequestVote,
				Index:   logIdx,
				LogTerm: logTerm,
			})
		}
	}
}

// becomeFollower transform this peer's state to Follower
func (r *Raft) becomeFollower(term uint64, lead uint64) {
	r.State = StateFollower
	r.Term = term
	r.Lead = lead
	r.Vote = None
	r.electionElapsed = 0
	r.randomElectionTimeout = r.electionTimeout + rand.Intn(r.electionTimeout)
}

// becomeCandidate transform this peer's state to candidate
func (r *Raft) becomeCandidate() {
	r.State = StateCandidate
	r.Term++
	r.Vote = r.id
	r.votes = make(map[uint64]bool)
	r.votes[r.id] = true
	r.electionElapsed = 0
	r.randomElectionTimeout = r.electionTimeout + rand.Intn(r.electionTimeout)
}

// becomeLeader transform this peer's state to leader
func (r *Raft) becomeLeader() {
	// Your Code Here (2A).
	// NOTE: Leader should propose a noop entry on its term
	r.State = StateLeader
	r.Lead = r.id
	r.heartbeatElapsed = 0

	lastIdx := r.RaftLog.LastIndex()
	for id := range r.Prs {
		if id == r.id {
			r.Prs[id].Match = lastIdx
			r.Prs[id].Next = lastIdx + 1
		} else {
			r.Prs[id].Next = lastIdx + 1
			r.Prs[id].Match = 0
		}
	}

	r.RaftLog.entries = append(r.RaftLog.entries, &pb.Entry{
		Term:  r.Term,
		Index: r.RaftLog.LastIndex() + 1,
		Data:  nil,
	})
	r.Prs[r.id].Match = r.RaftLog.LastIndex()
	r.Prs[r.id].Next = r.RaftLog.LastIndex() + 1

	r.maybeCommit()

	for id := range r.Prs {
		if id != r.id {
			r.sendAppend(id)
		}
	}
}

// Step the entrance of handle message, see `MessageType`
// on `eraftpb.proto` for what msgs should be handled
func (r *Raft) Step(m *pb.Message) error {
	// Your Code Here (2A).
	if m.MsgType == pb.MessageType_MsgHup && r.State != StateLeader {
		r.campaign()
		return nil
	}

	if m.MsgType == pb.MessageType_MsgBeat {
		if r.State == StateLeader {
			for id := range r.Prs {
				if id != r.id {
					r.sendHeartbeat(id)
				}
			}
		}
		return nil
	}

	if m.MsgType == pb.MessageType_MsgPropose && r.State == StateLeader {
		r.handlePropose(m)
		return nil
	}

	if m.Term > r.Term {
		r.becomeFollower(m.Term, None)
	} else if m.Term < r.Term {
		return nil
	}

	switch r.State {
	case StateFollower:
		switch m.MsgType {
		case pb.MessageType_MsgRequestVote:
			r.handleRequestVote(m)
		case pb.MessageType_MsgAppend:
			r.handleAppendEntries(m)
		case pb.MessageType_MsgHeartbeat:
			r.handleHeartbeat(m)
		case pb.MessageType_MsgSnapshot:
			r.handleSnapshot(m)
		}

	case StateCandidate:
		switch m.MsgType {
		case pb.MessageType_MsgRequestVote:
			r.handleRequestVote(m)
		case pb.MessageType_MsgRequestVoteResponse:
			r.handleRequestVoteResp(m)
		case pb.MessageType_MsgAppend:
			r.becomeFollower(m.Term, m.From)
		case pb.MessageType_MsgHeartbeat:
			r.becomeFollower(m.Term, m.From)
		case pb.MessageType_MsgSnapshot:
			r.becomeFollower(m.Term, m.From)
			r.handleSnapshot(m)
		}
	case StateLeader:
		switch m.MsgType {
		case pb.MessageType_MsgPropose:
			r.handlePropose(m)
		case pb.MessageType_MsgAppendResponse:
			r.handleAppendResponse(m)
		case pb.MessageType_MsgHeartbeatResponse:
			r.handleHeartbeatResponse(m)
		case pb.MessageType_MsgRequestVote:
			r.handleRequestVote(m)
		}
	}
	return nil
}

// handleAppendEntries handle AppendEntries RPC request
func (r *Raft) handleAppendEntries(m *pb.Message) {
	r.Lead = m.From
	r.electionElapsed = 0

	if m.Index > r.RaftLog.LastIndex() {
		r.msgs = append(r.msgs, &pb.Message{
			MsgType: pb.MessageType_MsgAppendResponse,
			From:    r.id,
			To:      m.From,
			Term:    r.Term,
			Reject:  true,
			Index:   0,
		})
		return
	}

	term, err := r.RaftLog.Term(m.Index)
	if err != nil || term != m.LogTerm {
		r.msgs = append(r.msgs, &pb.Message{
			MsgType: pb.MessageType_MsgAppendResponse,
			From:    r.id,
			To:      m.From,
			Term:    r.Term,
			Reject:  true,
			Index:   0,
		})
		return
	}

	offset := r.RaftLog.entries[0].Index
	for i, entry := range m.Entries {
		idx := entry.Index - offset
		if idx < uint64(len(r.RaftLog.entries)) {
			if r.RaftLog.entries[idx].Term != entry.Term {
				r.RaftLog.entries = r.RaftLog.entries[:idx]
				r.RaftLog.stabled = min(r.RaftLog.stabled, entry.Index-1)
				for _, entry := range m.Entries[i:] {
					r.RaftLog.entries = append(r.RaftLog.entries, entry)
				}
				break
			}
		} else {
			for _, entry := range m.Entries[i:] {
				r.RaftLog.entries = append(r.RaftLog.entries, entry)
			}
			break
		}
	}

	if m.Commit > r.RaftLog.committed {
		lastNewIdx := m.Index + uint64(len(m.Entries))
		if m.Commit < lastNewIdx {
			r.RaftLog.committed = m.Commit
		} else {
			r.RaftLog.committed = lastNewIdx
		}
	}

	r.msgs = append(r.msgs, &pb.Message{
		From:    r.id,
		To:      m.From,
		Term:    r.Term,
		MsgType: pb.MessageType_MsgAppendResponse,
		Reject:  false,
		Index:   r.RaftLog.LastIndex(),
	})
}

// handleHeartbeat handle Heartbeat RPC request
func (r *Raft) handleHeartbeat(m *pb.Message) {
	r.Lead = m.From
	r.electionElapsed = 0

	r.msgs = append(r.msgs,
		&pb.Message{
			From:    r.id,
			To:      m.From,
			Term:    r.Term,
			MsgType: pb.MessageType_MsgHeartbeatResponse,
		})
}

func (r *Raft) handleRequestVote(m *pb.Message) {
	lastIdx := r.RaftLog.LastIndex()
	lastTerm, _ := r.RaftLog.Term(lastIdx)

	if m.LogTerm < lastTerm || (m.LogTerm == lastTerm && m.Index < lastIdx) {
		r.msgs = append(r.msgs, &pb.Message{
			From:    r.id,
			To:      m.From,
			Term:    r.Term,
			MsgType: pb.MessageType_MsgRequestVoteResponse,
			Reject:  true,
		})
		return
	}

	if r.Vote == None || r.Vote == m.From {
		r.Vote = m.From
		r.msgs = append(r.msgs, &pb.Message{
			From:    r.id,
			To:      m.From,
			Term:    r.Term,
			MsgType: pb.MessageType_MsgRequestVoteResponse,
			Reject:  false,
		})
	} else {
		r.msgs = append(r.msgs, &pb.Message{
			From:    r.id,
			To:      m.From,
			Term:    r.Term,
			MsgType: pb.MessageType_MsgRequestVoteResponse,
			Reject:  true,
		})
	}
}

func (r *Raft) handleRequestVoteResp(m *pb.Message) {
	r.votes[m.From] = !m.Reject
	voteCount := 0
	for _, v := range r.votes {
		if v {
			voteCount++
		}
	}
	if voteCount > len(r.Prs)/2 {
		r.becomeLeader()
	} else if len(r.votes) == len(r.Prs) {
		r.becomeFollower(r.Term, None)
	}
}

func (r *Raft) handlePropose(m *pb.Message) {
	for _, entry := range m.Entries {
		entry.Term = r.Term
		entry.Index = r.RaftLog.LastIndex() + 1
		r.RaftLog.entries = append(r.RaftLog.entries, entry)
	}

	lastIdx := r.RaftLog.LastIndex()
	r.Prs[r.id].Match = lastIdx
	r.Prs[r.id].Next = lastIdx + 1

	r.maybeCommit()

	for id := range r.Prs {
		if id != r.id {
			r.sendAppend(id)
		}
	}
}

func (r *Raft) handleAppendResponse(m *pb.Message) {
	success := !m.Reject
	if success {
		r.Prs[m.From].Match = m.Index
		r.Prs[m.From].Next = m.Index + 1
	} else {
		r.Prs[m.From].Next--
		r.sendAppend(m.From)
	}

	if r.maybeCommit() {
		for id := range r.Prs {
			if id != r.id {
				r.sendAppend(id)
			}
		}
	}
}

func (r *Raft) maybeCommit() bool {
	for idx := r.RaftLog.committed + 1; idx <= r.RaftLog.LastIndex(); idx++ {
		term, _ := r.RaftLog.Term(idx)
		if term != r.Term {
			continue
		}
		count := 0
		for _, pr := range r.Prs {
			if pr.Match >= idx {
				count++
			}
		}
		if count > len(r.Prs)/2 {
			r.RaftLog.committed = idx
			return true
		}
	}
	return false
}

func (r *Raft) handleHeartbeatResponse(m *pb.Message) {
	if r.Prs[m.From].Match < r.RaftLog.LastIndex() {
		r.sendAppend(m.From)
	}
}

// handleSnapshot handle Snapshot RPC request
func (r *Raft) handleSnapshot(m *pb.Message) {
	snapshot := m.Snapshot
	if snapshot.Metadata.Index <= r.RaftLog.committed {
		return
	}
	r.RaftLog.entries = []*pb.Entry{{Term: snapshot.Metadata.Term, Index: snapshot.Metadata.Index}}
	r.RaftLog.stabled = snapshot.Metadata.Index
	r.RaftLog.committed = snapshot.Metadata.Index
	r.RaftLog.applied = snapshot.Metadata.Index
	r.RaftLog.pendingSnapshot = snapshot

	r.Prs = make(map[uint64]*Progress)
	for _, node := range snapshot.Metadata.ConfState.Nodes {
		r.Prs[node] = &Progress{Next: snapshot.Metadata.Index + 1}
	}
	r.Lead = m.From
}

func (r *Raft) sendSnapshot(to uint64) {
	snapshot, err := r.RaftLog.storage.Snapshot()
	if err == ErrSnapshotTemporarilyUnavailable {
		return
	}
	if err != nil {
		return
	}
	r.msgs = append(r.msgs,
		&pb.Message{MsgType: pb.MessageType_MsgSnapshot, From: r.id, To: to, Term: r.Term, Snapshot: snapshot},
	)
	r.Prs[to].Next = snapshot.Metadata.Index + 1
}

// addNode add a new node to raft group
func (r *Raft) addNode(id uint64) {
	// Your Code Here (3A).
}

// removeNode remove a node from raft group
func (r *Raft) removeNode(id uint64) {
	// Your Code Here (3A).
}
