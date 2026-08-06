package raft

import (
	"errors"
	"testing"

	pb "github.com/Aetherance/kv/proto/pkg/raftpb"
)

func requireMessage(t *testing.T, r *Raft, typ pb.MessageType, term uint64, reject bool) *pb.Message {
	t.Helper()
	messages := r.readMessages()
	if len(messages) != 1 {
		t.Fatalf("messages=%+v, want one %s", messages, typ)
	}
	m := messages[0]
	if m.MsgType != typ || m.Term != term || m.Reject != reject {
		t.Fatalf("message=%+v, want type=%s term=%d reject=%v", m, typ, term, reject)
	}
	return m
}

func TestPreVoteReadyLifecycle(t *testing.T) {
	storage := NewMemoryState()
	if err := storage.SetHardState(&pb.HardState{Term: 4, Vote: 2}); err != nil {
		t.Fatalf("set hard state: %v", err)
	}
	rn, err := NewRawNode(newTestConfig(1, []uint64{1, 2, 3}, 10, 1, storage))
	if err != nil {
		t.Fatalf("new raw node: %v", err)
	}
	if err := rn.Campaign(); err != nil {
		t.Fatalf("campaign: %v", err)
	}
	if rn.Raft.State != StatePreCandidate || rn.Raft.Term != 4 || rn.Raft.Vote != 2 {
		t.Fatalf("pre-campaign changed hard state: state=%s term=%d vote=%d",
			rn.Raft.State, rn.Raft.Term, rn.Raft.Vote)
	}
	rd := rn.Ready()
	if rd.HardState != nil || rd.SoftState == nil || rd.SoftState.RaftState != StatePreCandidate {
		t.Fatalf("pre-campaign Ready=%+v, want only PreCandidate SoftState", rd)
	}
	if len(rd.Messages) != 2 {
		t.Fatalf("messages=%d, want 2", len(rd.Messages))
	}
	for _, m := range rd.Messages {
		if m.MsgType != pb.MessageType_MsgPreVote || m.Term != 5 {
			t.Errorf("message=%+v, want PreVote in term 5", m)
		}
	}

	if err := rn.Step(&pb.Message{From: 2, To: 1, Term: 6,
		MsgType: pb.MessageType_MsgPreVoteResponse, Reject: true}); err != nil {
		t.Fatalf("step higher-term rejection: %v", err)
	}
	if !rn.HasReady() {
		t.Fatal("higher-term rejection changed state without producing Ready")
	}
	rd = rn.Ready()
	if rd.HardState == nil || rd.HardState.Term != 6 || rd.HardState.Vote != None ||
		rd.SoftState == nil || rd.SoftState.RaftState != StateFollower {
		t.Fatalf("rejection Ready=%+v, want Follower and hard state term=6 vote=None", rd)
	}
	if rn.HasReady() {
		t.Fatal("Ready remained pending after state snapshots were consumed")
	}
	if err := rn.Step(&pb.Message{From: 4, To: 1, Term: 7,
		MsgType: pb.MessageType_MsgPreVoteResponse}); !errors.Is(err, ErrStepPeerNotFound) {
		t.Fatalf("unknown peer error=%v, want %v", err, ErrStepPeerNotFound)
	}
}

func TestPreVoteRequestEligibilityAndImmutability(t *testing.T) {
	tests := []struct {
		name    string
		state   StateType
		msgTerm uint64
		vote    uint64
		lead    uint64
		stale   bool
		reject  bool
	}{
		{name: "future/follower", state: StateFollower, msgTerm: 4, vote: 2, lead: 3},
		{name: "future/pre-candidate", state: StatePreCandidate, msgTerm: 4, vote: 2, lead: 3},
		{name: "future/candidate", state: StateCandidate, msgTerm: 4, vote: 2, lead: 3},
		{name: "future/leader", state: StateLeader, msgTerm: 4, vote: 2, lead: 3},
		{name: "same-term/available", state: StateFollower, msgTerm: 3},
		{name: "same-term/voted-for-requester", state: StateFollower, msgTerm: 3, vote: 2},
		{name: "same-term/voted-for-other", state: StateFollower, msgTerm: 3, vote: 3, reject: true},
		{name: "same-term/leader-known", state: StateFollower, msgTerm: 3, lead: 3, reject: true},
		{name: "stale-log", state: StateFollower, msgTerm: 4, vote: 2, stale: true, reject: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			storage := NewMemoryState()
			if tt.stale {
				storage = newMemoryStateWithEnts([]*pb.Entry{{}, {Index: 1, Term: 3}})
			}
			r := newTestRaft(1, []uint64{1, 2, 3}, 10, 1, storage)
			r.State, r.Term, r.Vote, r.Lead, r.electionElapsed = tt.state, 3, tt.vote, tt.lead, 4
			if err := r.Step(&pb.Message{From: 2, To: 1, Term: tt.msgTerm,
				MsgType: pb.MessageType_MsgPreVote}); err != nil {
				t.Fatalf("step: %v", err)
			}
			if r.State != tt.state || r.Term != 3 || r.Vote != tt.vote || r.Lead != tt.lead || r.electionElapsed != 4 {
				t.Fatalf("receiver mutated: state=%s term=%d vote=%d lead=%d elapsed=%d",
					r.State, r.Term, r.Vote, r.Lead, r.electionElapsed)
			}
			responseTerm := uint64(3)
			if !tt.reject {
				responseTerm = tt.msgTerm
			}
			requireMessage(t, r, pb.MessageType_MsgPreVoteResponse, responseTerm, tt.reject)
		})
	}
}

func TestPreVoteResponsesAndTimeout(t *testing.T) {
	r := newTestRaft(1, []uint64{1, 2, 3}, 10, 1, NewMemoryState())
	r.Term, r.Vote = 2, 3
	r.campaign()
	r.readMessages()

	r.Step(&pb.Message{From: 2, To: 1, Term: 2,
		MsgType: pb.MessageType_MsgPreVoteResponse, Reject: true})
	if r.State != StatePreCandidate || r.Term != 2 {
		t.Fatalf("rejection moved node to state=%s term=%d", r.State, r.Term)
	}
	r.Step(&pb.Message{From: 2, To: 1, Term: 3, MsgType: pb.MessageType_MsgPreVoteResponse})
	if r.State != StateCandidate || r.Term != 3 || r.Vote != 1 {
		t.Fatalf("quorum did not start formal election: state=%s term=%d vote=%d", r.State, r.Term, r.Vote)
	}
	messages := r.readMessages()
	if len(messages) != 2 {
		t.Fatalf("messages=%d, want 2 RequestVote messages", len(messages))
	}
	for _, m := range messages {
		if m.MsgType != pb.MessageType_MsgRequestVote || m.Term != 3 {
			t.Errorf("message=%+v, want RequestVote in term 3", m)
		}
	}

	r.Term, r.Vote = 3, 2
	r.campaign()
	r.readMessages()
	r.randomElectionTimeout = 1
	r.tick()
	if r.State != StatePreCandidate || r.Term != 3 || r.Vote != 2 {
		t.Fatalf("retry changed hard state: state=%s term=%d vote=%d", r.State, r.Term, r.Vote)
	}
	for _, m := range r.readMessages() {
		if m.MsgType != pb.MessageType_MsgPreVote || m.Term != 4 {
			t.Errorf("retry message=%+v, want PreVote in term 4", m)
		}
	}
}

func TestPreCandidateProcessesLeaderRPC(t *testing.T) {
	tests := []struct {
		name     string
		message  *pb.Message
		response pb.MessageType
		last     uint64
		commit   uint64
	}{
		{name: "heartbeat", message: &pb.Message{MsgType: pb.MessageType_MsgHeartbeat},
			response: pb.MessageType_MsgHeartbeatResponse},
		{name: "append", message: &pb.Message{MsgType: pb.MessageType_MsgAppend, Index: 0, LogTerm: 0,
			Entries: []*pb.Entry{{Index: 1, Term: 2}}, Commit: 1},
			response: pb.MessageType_MsgAppendResponse, last: 1, commit: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newTestRaft(1, []uint64{1, 2, 3}, 10, 1, NewMemoryState())
			r.Term, r.Vote = 2, 2
			r.becomePreCandidate()
			tt.message.From, tt.message.To, tt.message.Term = 2, 1, 2
			if err := r.Step(tt.message); err != nil {
				t.Fatalf("step: %v", err)
			}
			if r.State != StateFollower || r.Term != 2 || r.Vote != 2 || r.Lead != 2 ||
				r.RaftLog.LastIndex() != tt.last || r.RaftLog.committed != tt.commit {
				t.Fatalf("state=%s term=%d vote=%d lead=%d last=%d commit=%d",
					r.State, r.Term, r.Vote, r.Lead, r.RaftLog.LastIndex(), r.RaftLog.committed)
			}
			response := requireMessage(t, r, tt.response, 2, false)
			if tt.last > 0 && response.Index != tt.last {
				t.Fatalf("response index=%d, want %d", response.Index, tt.last)
			}
		})
	}
}

func TestPreVoteIsolatedNodeDoesNotIncreaseClusterTerm(t *testing.T) {
	n := newNetwork(nil, nil, nil)
	n.send(&pb.Message{From: 1, To: 1, MsgType: pb.MessageType_MsgHup})
	leader, follower, isolated := n.peers[1].(*Raft), n.peers[2].(*Raft), n.peers[3].(*Raft)
	term := leader.Term

	n.isolate(3)
	for i := 0; i < 5; i++ {
		n.send(&pb.Message{From: 3, To: 3, MsgType: pb.MessageType_MsgHup})
	}
	if leader.State != StateLeader || leader.Term != term || follower.Term != term ||
		isolated.State != StatePreCandidate || isolated.Term != term {
		t.Fatalf("partition changed majority: leader=%s/%d followerTerm=%d isolated=%s/%d",
			leader.State, leader.Term, follower.Term, isolated.State, isolated.Term)
	}

	n.recover()
	n.send(&pb.Message{From: 1, To: 3, Term: term, MsgType: pb.MessageType_MsgHeartbeat})
	if isolated.State != StateFollower || isolated.Term != term {
		t.Fatalf("recovered node=%s/%d, want Follower/%d", isolated.State, isolated.Term, term)
	}
}
