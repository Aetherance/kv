package raft

import (
	"errors"
	"testing"

	pb "github.com/Aetherance/kv/proto/pkg/raftpb"
)

func TestLearnerDoesNotAffectQuorum(t *testing.T) {
	r := newTestRaft(1, []uint64{1, 2, 3}, 10, 1, NewMemoryState())
	r.becomeCandidate()
	r.becomeLeader()
	r.addLearner(4)

	lastIndex := r.RaftLog.LastIndex()
	r.Prs[4].Match = lastIndex
	r.Prs[4].Next = lastIndex + 1
	if r.maybeCommit() || r.RaftLog.committed != 0 {
		t.Fatalf("leader plus learner committed without a voter quorum: %d", r.RaftLog.committed)
	}

	r.Prs[2].Match = lastIndex
	r.Prs[2].Next = lastIndex + 1
	if !r.maybeCommit() || r.RaftLog.committed != lastIndex {
		t.Fatalf("voter quorum did not commit index %d: committed=%d", lastIndex, r.RaftLog.committed)
	}
}

func TestLearnerCannotCampaignOrVote(t *testing.T) {
	storage := NewMemoryState()
	storage.snapshot.Metadata.ConfState = &pb.ConfState{Voters: []uint64{1}, Learners: []uint64{2}}
	r := newRaft(&Config{ID: 2, ElectionTick: 10, HeartbeatTick: 1, PersistentState: storage})

	if err := r.Step(&pb.Message{MsgType: pb.MessageType_MsgHup}); err != nil {
		t.Fatalf("campaign: %v", err)
	}
	if r.State != StateFollower || r.Term != 0 {
		t.Fatalf("learner campaigned: state=%s term=%d", r.State, r.Term)
	}
	if err := r.Step(&pb.Message{MsgType: pb.MessageType_MsgRequestVote, From: 1, To: 2, Term: 1}); err != nil {
		t.Fatalf("vote request: %v", err)
	}
	if len(r.msgs) != 1 || !r.msgs[0].Reject {
		t.Fatalf("learner vote response = %+v, want rejection", r.msgs)
	}
}

func TestPromotingLearnerPreservesProgress(t *testing.T) {
	r := newTestRaft(1, []uint64{1}, 10, 1, NewMemoryState())
	r.becomeCandidate()
	r.becomeLeader()
	r.addLearner(2)
	r.Prs[2].Match = 9
	r.Prs[2].Next = 10
	r.Prs[2].RecentActive = true

	r.addNode(2)
	if !r.Tracker.isVoter(2) || r.Tracker.isLearner(2) || r.Prs[2].IsLearner {
		t.Fatalf("promoted member has wrong tracker state: %+v", r.Prs[2])
	}
	if r.Prs[2].Match != 9 || r.Prs[2].Next != 10 || !r.Prs[2].RecentActive {
		t.Fatalf("promotion reset replication progress: %+v", r.Prs[2])
	}
}

func TestOnlyOneUnappliedConfChangeIsAccepted(t *testing.T) {
	r := newTestRaft(1, []uint64{1}, 10, 1, NewMemoryState())
	r.becomeCandidate()
	r.becomeLeader()
	change := &pb.Entry{EntryType: pb.EntryType_EntryConfChange}
	proposal := func(entries ...*pb.Entry) error {
		return r.Step(&pb.Message{MsgType: pb.MessageType_MsgPropose, From: 1, Entries: entries})
	}

	if err := proposal(change); err != nil {
		t.Fatalf("first conf change: %v", err)
	}
	lastIndex := r.RaftLog.LastIndex()
	if err := proposal(&pb.Entry{EntryType: pb.EntryType_EntryConfChange}); !errors.Is(err, ErrConfChangePending) {
		t.Fatalf("second conf change error = %v, want ErrConfChangePending", err)
	}
	if r.RaftLog.LastIndex() != lastIndex {
		t.Fatalf("rejected conf change changed last index to %d", r.RaftLog.LastIndex())
	}

	r.RaftLog.applied = r.PendingConfIndex
	if err := proposal(&pb.Entry{EntryType: pb.EntryType_EntryConfChange}); err != nil {
		t.Fatalf("conf change after apply: %v", err)
	}
}
