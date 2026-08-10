package raft

import (
	"bytes"
	"testing"

	pb "github.com/Aetherance/kv/proto/pkg/raftpb"
)

func TestReadIndexWaitsForCurrentTermCommit(t *testing.T) {
	r := newTestRaft(1, []uint64{1, 2, 3}, 10, 1, NewMemoryState())
	r.becomeCandidate()
	r.becomeLeader()
	r.readMessages()

	context := []byte("read-before-noop-commit")
	if err := r.Step(&pb.Message{
		MsgType: pb.MessageType_MsgReadIndex,
		From:    1,
		To:      1,
		Term:    r.Term,
		Context: context,
	}); err != nil {
		t.Fatalf("read index: %v", err)
	}
	if len(r.pendingReadIndexMessages) != 1 {
		t.Fatalf("pending reads = %d, want 1", len(r.pendingReadIndexMessages))
	}
	if len(r.readStates) != 0 || len(r.readMessages()) != 0 {
		t.Fatal("read completed before the current-term entry committed")
	}

	if err := r.Step(&pb.Message{
		MsgType: pb.MessageType_MsgAppendResponse,
		From:    2,
		To:      1,
		Term:    r.Term,
		Index:   1,
	}); err != nil {
		t.Fatalf("append response: %v", err)
	}
	if r.RaftLog.committed != 1 {
		t.Fatalf("committed = %d, want 1", r.RaftLog.committed)
	}

	var heartbeat *pb.Message
	for _, message := range r.readMessages() {
		if message.MsgType == pb.MessageType_MsgHeartbeat && bytes.Equal(message.Context, context) {
			heartbeat = message
			break
		}
	}
	if heartbeat == nil {
		t.Fatal("current-term commit did not start read-index heartbeat")
	}
	if err := r.Step(&pb.Message{
		MsgType: pb.MessageType_MsgHeartbeatResponse,
		From:    heartbeat.To,
		To:      1,
		Term:    r.Term,
		Context: context,
	}); err != nil {
		t.Fatalf("heartbeat response: %v", err)
	}
	assertReadStates(t, r.readStates, 1, context)
}

func TestReadIndexRequiresQuorumAndBatchesEarlierRequests(t *testing.T) {
	r := newTestRaft(1, []uint64{1, 2, 3}, 10, 1, NewMemoryState())
	r.becomeCandidate()
	r.becomeLeader()
	if err := r.Step(&pb.Message{
		MsgType: pb.MessageType_MsgAppendResponse,
		From:    2,
		To:      1,
		Term:    r.Term,
		Index:   1,
	}); err != nil {
		t.Fatal(err)
	}
	r.readMessages()

	first := []byte("first")
	second := []byte("second")
	for _, context := range [][]byte{first, second} {
		if err := r.Step(&pb.Message{
			MsgType: pb.MessageType_MsgReadIndex,
			From:    1,
			To:      1,
			Term:    r.Term,
			Context: context,
		}); err != nil {
			t.Fatal(err)
		}
	}
	r.readMessages()
	if len(r.readStates) != 0 {
		t.Fatal("read completed without a quorum response")
	}

	if err := r.Step(&pb.Message{
		MsgType: pb.MessageType_MsgHeartbeatResponse,
		From:    2,
		To:      1,
		Term:    r.Term,
		Context: second,
	}); err != nil {
		t.Fatal(err)
	}
	if len(r.readStates) != 2 {
		t.Fatalf("read states = %d, want 2", len(r.readStates))
	}
	assertReadStates(t, r.readStates[:1], 1, first)
	assertReadStates(t, r.readStates[1:], 1, second)

	if err := r.Step(&pb.Message{
		MsgType: pb.MessageType_MsgHeartbeatResponse,
		From:    3,
		To:      1,
		Term:    r.Term,
		Context: first,
	}); err != nil {
		t.Fatal(err)
	}
	if len(r.readStates) != 2 {
		t.Fatal("late response completed a read twice")
	}
}

func TestFollowerForwardsReadIndexAndReadsLocally(t *testing.T) {
	network := newNetwork(nil, nil, nil)
	network.send(&pb.Message{From: 1, To: 1, MsgType: pb.MessageType_MsgHup})

	leader := network.peers[1].(*Raft)
	follower := network.peers[2].(*Raft)
	context := []byte("follower-read")
	network.send(&pb.Message{
		MsgType: pb.MessageType_MsgReadIndex,
		From:    2,
		To:      2,
		Term:    follower.Term,
		Context: context,
	})

	if len(leader.readStates) != 0 {
		t.Fatal("leader produced a local ReadState for a follower request")
	}
	assertReadStates(t, follower.readStates, leader.RaftLog.committed, context)
}

func TestHeartbeatCarriesContextAndAdvancesFollowerCommit(t *testing.T) {
	follower := newTestRaft(2, []uint64{1, 2, 3}, 10, 1, NewMemoryState())
	follower.Term = 1
	follower.becomeFollower(1, 1)
	follower.RaftLog.entries = append(follower.RaftLog.entries, &pb.Entry{Index: 1, Term: 1})
	context := []byte("heartbeat-context")

	if err := follower.Step(&pb.Message{
		MsgType: pb.MessageType_MsgHeartbeat,
		From:    1,
		To:      2,
		Term:    1,
		Commit:  1,
		Context: context,
	}); err != nil {
		t.Fatal(err)
	}
	if follower.RaftLog.committed != 1 {
		t.Fatalf("committed = %d, want 1", follower.RaftLog.committed)
	}
	messages := follower.readMessages()
	if len(messages) != 1 || messages[0].MsgType != pb.MessageType_MsgHeartbeatResponse {
		t.Fatalf("messages = %v, want one heartbeat response", messages)
	}
	if !bytes.Equal(messages[0].Context, context) {
		t.Fatalf("response context = %q, want %q", messages[0].Context, context)
	}
}

func TestReadIndexStateClearedOnStepDown(t *testing.T) {
	r := newTestRaft(1, []uint64{1, 2, 3}, 10, 1, NewMemoryState())
	r.becomeCandidate()
	r.becomeLeader()
	if err := r.Step(&pb.Message{
		MsgType: pb.MessageType_MsgAppendResponse,
		From:    2,
		To:      1,
		Term:    r.Term,
		Index:   1,
	}); err != nil {
		t.Fatal(err)
	}
	r.readMessages()
	context := []byte("stale-read")
	if err := r.Step(&pb.Message{
		MsgType: pb.MessageType_MsgReadIndex,
		From:    1,
		To:      1,
		Term:    r.Term,
		Context: context,
	}); err != nil {
		t.Fatal(err)
	}
	oldTerm := r.Term
	if err := r.Step(&pb.Message{
		MsgType: pb.MessageType_MsgHeartbeat,
		From:    2,
		To:      1,
		Term:    oldTerm + 1,
	}); err != nil {
		t.Fatal(err)
	}
	if len(r.readOnly.pending) != 0 || len(r.readOnly.queue) != 0 {
		t.Fatal("step-down retained pending read-index state")
	}
	if err := r.Step(&pb.Message{
		MsgType: pb.MessageType_MsgHeartbeatResponse,
		From:    3,
		To:      1,
		Term:    oldTerm,
		Context: context,
	}); err != nil {
		t.Fatal(err)
	}
	if len(r.readStates) != 0 {
		t.Fatal("stale response completed a read after step-down")
	}
}

func assertReadStates(t *testing.T, states []ReadState, index uint64, context []byte) {
	t.Helper()
	if len(states) != 1 {
		t.Fatalf("read states = %d, want 1", len(states))
	}
	if states[0].Index != index || !bytes.Equal(states[0].RequestCtx, context) {
		t.Fatalf("read state = %+v, want index %d context %q", states[0], index, context)
	}
}
