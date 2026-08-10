package raft

import (
	"testing"

	pb "github.com/Aetherance/kv/proto/pkg/raftpb"
)

func newQuorumTestRaft(id uint64, peers []uint64) *Raft {
	config := newTestConfig(id, peers, 10, 1, NewMemoryState())
	config.CheckQuorum = true
	config.LeaderLease = true
	return newRaft(config)
}

func becomeCommittedQuorumLeader(t *testing.T, r *Raft) {
	t.Helper()
	r.becomeCandidate()
	r.becomeLeader()
	if len(r.Prs) == 1 {
		return
	}
	if err := r.Step(&pb.Message{
		MsgType: pb.MessageType_MsgAppendResponse,
		From:    2,
		To:      r.id,
		Term:    r.Term,
		Index:   r.RaftLog.LastIndex(),
	}); err != nil {
		t.Fatalf("commit leader noop: %v", err)
	}
	if !r.committedEntryInCurrentTerm() {
		t.Fatal("leader did not commit an entry in its current term")
	}
}

func TestCheckQuorumStepsDownWithoutMajority(t *testing.T) {
	r := newQuorumTestRaft(1, []uint64{1, 2, 3})
	r.becomeCandidate()
	r.becomeLeader()

	for range r.electionTimeout {
		r.tick()
	}
	if r.State != StateFollower || r.Lead != None {
		t.Fatalf("isolated leader state=%s lead=%d, want follower with no leader", r.State, r.Lead)
	}
}

func TestCheckQuorumKeepsResponsiveLeader(t *testing.T) {
	r := newQuorumTestRaft(1, []uint64{1, 2, 3})
	becomeCommittedQuorumLeader(t, r)

	for range r.electionTimeout {
		r.tick()
	}
	if r.State != StateLeader {
		t.Fatalf("responsive leader stepped down: state=%s", r.State)
	}
}

func TestLeaderLeaseCompletesReadIndexWithoutHeartbeat(t *testing.T) {
	r := newQuorumTestRaft(1, []uint64{1, 2, 3})
	becomeCommittedQuorumLeader(t, r)
	if !r.hasLeaderLease() {
		t.Fatal("quorum acknowledgement did not establish leader lease")
	}
	r.readMessages()

	context := []byte("lease-read")
	if err := r.Step(&pb.Message{
		MsgType: pb.MessageType_MsgReadIndex,
		From:    r.id,
		To:      r.id,
		Term:    r.Term,
		Context: context,
	}); err != nil {
		t.Fatalf("read index: %v", err)
	}
	assertReadStates(t, r.readStates, r.RaftLog.committed, context)
	if messages := r.readMessages(); len(messages) != 0 {
		t.Fatalf("lease read sent messages: %+v", messages)
	}
}

func TestLeaderLeaseExpiresWithoutFreshQuorum(t *testing.T) {
	r := newQuorumTestRaft(1, []uint64{1, 2, 3})
	becomeCommittedQuorumLeader(t, r)
	for range r.electionTimeout {
		r.tick()
	}
	if r.State != StateLeader {
		t.Fatalf("leader stepped down while checking a responsive quorum: %s", r.State)
	}
	if r.hasLeaderLease() {
		t.Fatal("leader lease remained valid without a fresh quorum acknowledgement")
	}
	r.readMessages()

	context := []byte("expired-lease-read")
	if err := r.Step(&pb.Message{
		MsgType: pb.MessageType_MsgReadIndex,
		From:    r.id,
		To:      r.id,
		Term:    r.Term,
		Context: context,
	}); err != nil {
		t.Fatalf("read index: %v", err)
	}
	if len(r.readStates) != 0 {
		t.Fatal("expired lease completed read index without a heartbeat quorum")
	}
	for _, message := range r.readMessages() {
		if message.MsgType == pb.MessageType_MsgHeartbeat && string(message.Context) == string(context) {
			return
		}
	}
	t.Fatal("expired lease did not start a ReadIndex heartbeat round")
}

func TestLeaderLeaseRejectsElectionProbes(t *testing.T) {
	r := newQuorumTestRaft(1, []uint64{1, 2, 3})
	r.Term = 1
	r.becomeFollower(1, 2)

	for _, messageType := range []pb.MessageType{
		pb.MessageType_MsgPreVote,
		pb.MessageType_MsgRequestVote,
	} {
		if err := r.Step(&pb.Message{
			MsgType: messageType,
			From:    3,
			To:      1,
			Term:    2,
			Index:   r.RaftLog.LastIndex(),
			LogTerm: 0,
		}); err != nil {
			t.Fatalf("%s: %v", messageType, err)
		}
		messages := r.readMessages()
		if len(messages) != 1 || !messages[0].Reject {
			t.Fatalf("%s response=%+v, want one rejection", messageType, messages)
		}
		if r.Term != 1 || r.Lead != 2 {
			t.Fatalf("%s changed protected follower: term=%d lead=%d", messageType, r.Term, r.Lead)
		}
	}
}
