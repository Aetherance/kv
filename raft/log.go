package raft

import pb "github.com/Aetherance/kv/proto/pkg/raftpb"

// RaftLog manage the log entries, its struct look like:
//
//	snapshot/first.....applied....committed....stabled.....last
//	--------|------------------------------------------------|
//	                          log entries
//
// for simplify the RaftLog implement should manage all log entries
// that not truncated
type RaftLog struct {
	// state exposes entries that were stable before the current Ready batch.
	state PersistentState

	// committed is the highest log position that is known to be in
	// stable storage on a quorum of nodes.
	committed uint64

	// applied is the highest log position that the application has
	// been instructed to apply to its state machine.
	// Invariant: applied <= committed
	applied uint64

	// log entries with index <= stabled are persisted to storage.
	// It is used to record the logs that are not persisted by storage yet.
	// Everytime handling `Ready`, the unstabled logs will be included.
	stabled uint64

	// all entries that have not yet compact.
	entries []*pb.Entry

	// the incoming unstable snapshot, if any.
	// (Used in 2C)
	pendingSnapshot *pb.Snapshot

	// Your Data Here (2A).
}

// newLog returns a log recovered from the given persistent state.
// to the state that it just commits and applies the latest snapshot.
func newLog(state PersistentState) *RaftLog {
	if state == nil {
		return nil
	}

	hs, _, _ := state.InitialState()
	first, _ := state.FirstIndex()
	last, _ := state.LastIndex()

	entries := make([]*pb.Entry, 0)
	dummyTerm, _ := state.Term(first - 1)
	dummy := &pb.Entry{
		Index: first - 1,
		Term:  dummyTerm,
	}
	entries = append(entries, dummy)
	if first <= last {
		stored, _ := state.Entries(first, last+1)
		entries = append(entries, stored...)
	}

	return &RaftLog{
		state:     state,
		stabled:   last,
		applied:   entries[0].Index,
		committed: hs.Commit,
		entries:   entries,
	}
}

// We need to compact the log entries in some point of time like
// storage compact stabled log entries prevent the log entries
// grow unlimitedly in memory
func (l *RaftLog) maybeCompact() {
	first, err := l.state.FirstIndex()
	if err != nil {
		return
	}
	if first <= l.entries[0].Index+1 {
		return
	}
	compactIndex := first - 1
	term, err := l.state.Term(compactIndex)
	if err != nil {
		return
	}
	offset := l.entries[0].Index
	if compactIndex > l.LastIndex() {
		l.entries = []*pb.Entry{{Index: compactIndex, Term: term}}
		return
	}
	l.entries = append([]*pb.Entry{{Index: compactIndex, Term: term}}, l.entries[compactIndex+1-offset:]...)
}

// allEntries return all the entries not compacted.
// note, exclude any dummy entries from the return value.
// note, this is one of the test stub functions you need to implement.
func (l *RaftLog) allEntries() []*pb.Entry {
	return l.entries[1:]
}

// unstableEntries return all the unstable entries
func (l *RaftLog) unstableEntries() []*pb.Entry {
	offset := l.entries[0].Index
	if l.stabled >= l.LastIndex() {
		return []*pb.Entry{}
	}
	return l.entries[l.stabled+1-offset:]
}

// nextEnts returns all the committed but not applied entries
func (l *RaftLog) nextEnts() (ents []*pb.Entry) {
	offset := l.entries[0].Index
	return l.entries[l.applied+1-offset : l.committed+1-offset]
}

// LastIndex return the last index of the log entries
func (l *RaftLog) LastIndex() uint64 {
	return l.entries[len(l.entries)-1].Index
}

// Term return the term of the entry in the given index
func (l *RaftLog) Term(i uint64) (uint64, error) {
	offset := l.entries[0].Index
	if i < offset {
		return 0, ErrCompacted
	}
	idx := i - offset
	if idx >= uint64(len(l.entries)) {
		return 0, ErrUnavailable
	}
	return l.entries[idx].Term, nil
}
