// Copyright 2015 The etcd Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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

// ErrConfChangePending is returned when a second configuration change is
// proposed before the previous one has been applied.
var ErrConfChangePending = errors.New("raft configuration change already pending")

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

	// PersistentState provides the protocol state recovered before this Raft
	// instance starts. The Ready consumer owns persistence of newly produced
	// entries, HardState, and snapshots.
	PersistentState PersistentState
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

	if c.PersistentState == nil {
		return errors.New("persistent state cannot be nil")
	}

	return nil
}

type Raft struct {
	id uint64

	Term uint64
	Vote uint64

	// the log
	RaftLog *RaftLog

	// Tracker owns the voter/learner configuration and replication progress.
	Tracker *ProgressTracker
	// Prs is kept as a compatibility alias for tests and callers that inspect
	// progress. All membership mutation goes through Tracker.
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

	randElectionTimeout := c.ElectionTick + rand.Intn(c.ElectionTick)
	hs, cs, _ := c.PersistentState.InitialState()

	raftLog := newLog(c.PersistentState)
	tracker := newProgressTracker()
	if len(c.peers) > 0 {
		cs = &pb.ConfState{Voters: append([]uint64(nil), c.peers...)}
	}
	tracker.restore(cs, raftLog.LastIndex())
	if c.Applied > 0 {
		if c.Applied < raftLog.entries[0].Index || c.Applied > raftLog.committed {
			panic("applied index is outside the committed raft log")
		}
		raftLog.applied = c.Applied
	}

	return &Raft{
		id:                    c.ID,
		RaftLog:               raftLog,
		Tracker:               tracker,
		Prs:                   tracker.Progress,
		State:                 StateFollower,
		Term:                  hs.Term,
		Vote:                  hs.Vote,
		Lead:                  None,
		votes:                 tracker.Votes,
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
	if pr == nil || pr.PendingSnapshot != 0 {
		return false
	}
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
		if r.Tracker.isVoter(r.id) && r.electionElapsed >= r.randomElectionTimeout {
			r.electionElapsed = 0
			r.campaign()
		}
	case StateLeader:
		r.heartbeatElapsed++
		if r.heartbeatElapsed >= r.heartbeatTimeout {
			r.heartbeatElapsed = 0
			for id := range r.Prs {
				if id != r.id {
					r.sendHeartbeat(id)
				}
			}
		}
	}
}

func (r *Raft) campaign() {
	if !r.Tracker.isVoter(r.id) {
		return
	}
	r.becomeCandidate()
	if len(r.Tracker.Voters) == 1 {
		r.becomeLeader()
		return
	}
	logIdx := r.RaftLog.LastIndex()
	logTerm, _ := r.RaftLog.Term(logIdx)

	for prId := range r.Tracker.Voters {
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
	if !r.Tracker.isVoter(r.id) {
		return
	}
	r.State = StateCandidate
	r.Term++
	r.Vote = r.id
	r.Tracker.resetVotes()
	r.Tracker.recordVote(r.id, true)
	r.votes = r.Tracker.Votes
	r.electionElapsed = 0
	r.randomElectionTimeout = r.electionTimeout + rand.Intn(r.electionTimeout)
}

// becomeLeader transform this peer's state to leader
func (r *Raft) becomeLeader() {
	if !r.Tracker.isVoter(r.id) {
		return
	}
	// NOTE: Leader should propose a noop entry on its term
	r.State = StateLeader
	r.Lead = r.id
	r.heartbeatElapsed = 0
	// Conservatively block new configuration proposals until the existing log
	// tail is applied. It may contain a change proposed by the previous leader.
	r.PendingConfIndex = r.RaftLog.LastIndex()

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
	if m.MsgType == pb.MessageType_MsgHup && r.State != StateLeader {
		if !r.Tracker.isVoter(r.id) {
			return nil
		}
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

	if m.MsgType == pb.MessageType_MsgPropose {
		if r.State != StateLeader {
			return ErrProposalDropped
		}
		return r.handlePropose(m)
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
	if !r.Tracker.isVoter(r.id) || !r.Tracker.isVoter(m.From) {
		r.msgs = append(r.msgs, &pb.Message{
			From: r.id, To: m.From, Term: r.Term,
			MsgType: pb.MessageType_MsgRequestVoteResponse, Reject: true,
		})
		return
	}
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
	r.Tracker.recordVote(m.From, !m.Reject)
	r.votes = r.Tracker.Votes
	granted, rejected := r.Tracker.tallyVotes()
	if granted >= r.Tracker.quorum() {
		r.becomeLeader()
	} else if rejected >= r.Tracker.quorum() {
		r.becomeFollower(r.Term, None)
	}
}

func (r *Raft) handlePropose(m *pb.Message) error {
	pendingConfChange := r.PendingConfIndex > r.RaftLog.applied
	for _, entry := range m.Entries {
		if entry.EntryType != pb.EntryType_EntryConfChange {
			continue
		}
		if pendingConfChange {
			return ErrConfChangePending
		}
		pendingConfChange = true
	}

	for _, entry := range m.Entries {
		if entry.EntryType == pb.EntryType_EntryConfChange {
			r.PendingConfIndex = r.RaftLog.LastIndex() + 1
		}
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
	return nil
}

func (r *Raft) handleAppendResponse(m *pb.Message) {
	pr := r.Prs[m.From]
	if pr == nil {
		return
	}
	pr.RecentActive = true
	success := !m.Reject
	if success {
		pr.Match = m.Index
		pr.Next = m.Index + 1
		if pr.PendingSnapshot != 0 && m.Index >= pr.PendingSnapshot {
			pr.PendingSnapshot = 0
		}
	} else {
		if pr.Next > 1 {
			pr.Next--
		}
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
	index := r.Tracker.committed()
	if index <= r.RaftLog.committed {
		return false
	}
	term, err := r.RaftLog.Term(index)
	if err != nil || term != r.Term {
		return false
	}
	r.RaftLog.committed = index
	return true
}

func (r *Raft) handleHeartbeatResponse(m *pb.Message) {
	pr := r.Prs[m.From]
	if pr == nil {
		return
	}
	pr.RecentActive = true
	if pr.Match < r.RaftLog.LastIndex() {
		r.sendAppend(m.From)
	}
}

// handleSnapshot handle Snapshot RPC request
func (r *Raft) handleSnapshot(m *pb.Message) {
	snapshot := m.Snapshot
	if snapshot.Metadata.Index <= r.RaftLog.committed {
		r.msgs = append(r.msgs, &pb.Message{From: r.id, To: m.From, Term: r.Term,
			MsgType: pb.MessageType_MsgAppendResponse, Index: r.RaftLog.committed})
		return
	}
	r.RaftLog.entries = []*pb.Entry{{Term: snapshot.Metadata.Term, Index: snapshot.Metadata.Index}}
	r.RaftLog.stabled = snapshot.Metadata.Index
	r.RaftLog.committed = snapshot.Metadata.Index
	r.RaftLog.applied = snapshot.Metadata.Index
	r.RaftLog.pendingSnapshot = snapshot

	r.Tracker.restore(snapshot.Metadata.ConfState, snapshot.Metadata.Index)
	r.Prs = r.Tracker.Progress
	r.Lead = m.From
	r.msgs = append(r.msgs, &pb.Message{From: r.id, To: m.From, Term: r.Term,
		MsgType: pb.MessageType_MsgAppendResponse, Index: snapshot.Metadata.Index})
}

func (r *Raft) sendSnapshot(to uint64) {
	snapshot, err := r.RaftLog.state.Snapshot()
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
	r.Prs[to].PendingSnapshot = snapshot.Metadata.Index
}

// addNode add a new node to raft group
func (r *Raft) addNode(id uint64) {
	r.Tracker.makeVoter(id, r.RaftLog.LastIndex())
	r.Prs = r.Tracker.Progress
	if r.State == StateLeader && id == r.id {
		if pr := r.Prs[id]; pr != nil {
			pr.Match = r.RaftLog.LastIndex()
			pr.Next = pr.Match + 1
		}
	}
}

// addLearner adds a non-voting replication target.
func (r *Raft) addLearner(id uint64) {
	r.Tracker.addLearner(id, r.RaftLog.LastIndex())
	r.Prs = r.Tracker.Progress
}

// removeNode remove a node from raft group
func (r *Raft) removeNode(id uint64) {
	r.Tracker.remove(id)
	r.Prs = r.Tracker.Progress
	if len(r.Tracker.Voters) == 0 {
		return
	}
	if id == r.id && r.State == StateLeader {
		r.becomeFollower(r.Term, None)
		return
	}
	if r.State == StateLeader && r.maybeCommit() {
		for peerID := range r.Prs {
			if peerID != r.id {
				r.sendAppend(peerID)
			}
		}
	}
}
