package raft

import (
	"reflect"
	"testing"

	pb "github.com/Aetherance/kv/proto/pkg/raftpb"
)

// TestRawNodeStart ensures that a node can be started correctly, and can accept and commit
// proposals.
func TestRawNodeStart2AC(t *testing.T) {
	storage := NewMemoryState()
	rawNode, err := NewRawNode(newTestConfig(1, []uint64{1}, 10, 1, storage))
	if err != nil {
		t.Fatal(err)
	}
	rawNode.Campaign()
	rd := rawNode.Ready()
	storage.Append(rd.Entries)
	rawNode.Advance(&rd)

	rawNode.Propose([]byte("foo"))
	rd = rawNode.Ready()
	if el := len(rd.Entries); el != len(rd.CommittedEntries) || el != 1 {
		t.Errorf("got len(Entries): %+v, len(CommittedEntries): %+v, want %+v", el, len(rd.CommittedEntries), 1)
	}
	if !reflect.DeepEqual(rd.Entries[0].Data, rd.CommittedEntries[0].Data) || !reflect.DeepEqual(rd.Entries[0].Data, []byte("foo")) {
		t.Errorf("got %+v %+v , want %+v", rd.Entries[0].Data, rd.CommittedEntries[0].Data, []byte("foo"))
	}
	storage.Append(rd.Entries)
	rawNode.Advance(&rd)

	if rawNode.HasReady() {
		t.Errorf("unexpected Ready: %+v", rawNode.Ready())
	}
}

func TestRawNodeRestart2AC(t *testing.T) {
	entries := []*pb.Entry{
		{Term: 1, Index: 1},
		{Term: 1, Index: 2, Data: []byte("foo")},
	}
	st := pb.HardState{Term: 1, Commit: 1}

	want := Ready{
		Entries: []*pb.Entry{},
		// commit up to commit index in st
		CommittedEntries: entries[:st.Commit],
	}

	storage := NewMemoryState()
	storage.SetHardState(&st)
	storage.Append(entries)
	rawNode, err := NewRawNode(newTestConfig(1, nil, 10, 1, storage))
	if err != nil {
		t.Fatal(err)
	}
	rd := rawNode.Ready()
	if !readyEqual(rd, want) {
		t.Errorf("g = %#v,\n             w   %#v", rd, want)
	}
	rawNode.Advance(&rd)
	if rawNode.HasReady() {
		t.Errorf("unexpected Ready: %+v", rawNode.Ready())
	}
}

func TestRawNodeRestartFromSnapshot2C(t *testing.T) {
	snap := pb.Snapshot{
		Metadata: &pb.SnapshotMetadata{
			ConfState: &pb.ConfState{Nodes: []uint64{1, 2}},
			Index:     2,
			Term:      1,
		},
	}
	entries := []*pb.Entry{
		{Term: 1, Index: 3, Data: []byte("foo")},
	}
	st := pb.HardState{Term: 1, Commit: 3}

	want := Ready{
		Entries: []*pb.Entry{},
		// commit up to commit index in st
		CommittedEntries: entries,
	}

	s := NewMemoryState()
	s.SetHardState(&st)
	s.ApplySnapshot(&snap)
	s.Append(entries)
	rawNode, err := NewRawNode(newTestConfig(1, nil, 10, 1, s))
	if err != nil {
		t.Fatal(err)
	}
	if rd := rawNode.Ready(); !readyEqual(rd, want) {
		t.Errorf("g = %#v,\n             w   %#v", rd, want)
	} else {
		rawNode.Advance(&rd)
	}
	if rawNode.HasReady() {
		t.Errorf("unexpected Ready: %+v", rawNode.HasReady())
	}
}
