package raft

import (
	"sort"

	pb "github.com/Aetherance/kv/proto/pkg/raftpb"
)

// Progress represents a peer's replication progress in the leader's view.
// Learners are replicated to exactly like voters, but never take part in
// elections or quorum calculations.
type Progress struct {
	Match           uint64
	Next            uint64
	IsLearner       bool
	RecentActive    bool
	PendingSnapshot uint64
}

// ProgressTracker owns the active Raft configuration and the replication
// progress for every voter and learner. Its shape intentionally follows
// etcd/raft's tracker, while this first implementation supports only simple
// one-at-a-time configuration changes.
type ProgressTracker struct {
	Voters   map[uint64]struct{}
	Learners map[uint64]struct{}
	Progress map[uint64]*Progress
	Votes    map[uint64]bool
}

func newProgressTracker() *ProgressTracker {
	return &ProgressTracker{
		Voters:   make(map[uint64]struct{}),
		Learners: make(map[uint64]struct{}),
		Progress: make(map[uint64]*Progress),
		Votes:    make(map[uint64]bool),
	}
}

func (t *ProgressTracker) restore(cs *pb.ConfState, lastIndex uint64) {
	t.Voters = make(map[uint64]struct{})
	t.Learners = make(map[uint64]struct{})
	t.Progress = make(map[uint64]*Progress)
	if cs == nil {
		return
	}
	for _, id := range cs.Voters {
		if id == None {
			continue
		}
		t.Voters[id] = struct{}{}
		t.Progress[id] = &Progress{Next: lastIndex + 1}
	}
	for _, id := range cs.Learners {
		if id == None {
			continue
		}
		if _, voter := t.Voters[id]; voter {
			continue
		}
		t.Learners[id] = struct{}{}
		t.Progress[id] = &Progress{Next: lastIndex + 1, IsLearner: true}
	}
}

func (t *ProgressTracker) confState() *pb.ConfState {
	return &pb.ConfState{
		Voters:   sortedIDs(t.Voters),
		Learners: sortedIDs(t.Learners),
	}
}

func (t *ProgressTracker) voterNodes() []uint64 { return sortedIDs(t.Voters) }

func (t *ProgressTracker) learnerNodes() []uint64 { return sortedIDs(t.Learners) }

func (t *ProgressTracker) isVoter(id uint64) bool {
	_, ok := t.Voters[id]
	return ok
}

func (t *ProgressTracker) isLearner(id uint64) bool {
	_, ok := t.Learners[id]
	return ok
}

func (t *ProgressTracker) quorum() int { return len(t.Voters)/2 + 1 }

func (t *ProgressTracker) committed() uint64 {
	if len(t.Voters) == 0 {
		return 0
	}
	matches := make([]uint64, 0, len(t.Voters))
	for id := range t.Voters {
		if pr := t.Progress[id]; pr != nil {
			matches = append(matches, pr.Match)
		} else {
			matches = append(matches, 0)
		}
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i] < matches[j] })
	return matches[len(matches)-t.quorum()]
}

func (t *ProgressTracker) resetVotes() { t.Votes = make(map[uint64]bool) }

func (t *ProgressTracker) recordVote(id uint64, granted bool) {
	if !t.isVoter(id) {
		return
	}
	if _, seen := t.Votes[id]; !seen {
		t.Votes[id] = granted
	}
}

func (t *ProgressTracker) tallyVotes() (granted, rejected int) {
	for id, vote := range t.Votes {
		if !t.isVoter(id) {
			continue
		}
		if vote {
			granted++
		} else {
			rejected++
		}
	}
	return granted, rejected
}

func (t *ProgressTracker) addLearner(id, lastIndex uint64) {
	if id == None || t.isVoter(id) || t.isLearner(id) {
		return
	}
	t.Learners[id] = struct{}{}
	t.Progress[id] = &Progress{Next: lastIndex + 1, IsLearner: true}
}

func (t *ProgressTracker) makeVoter(id, lastIndex uint64) {
	if id == None || t.isVoter(id) {
		return
	}
	if pr := t.Progress[id]; pr != nil {
		delete(t.Learners, id)
		pr.IsLearner = false
		t.Voters[id] = struct{}{}
		return
	}
	t.Voters[id] = struct{}{}
	t.Progress[id] = &Progress{Next: lastIndex + 1}
}

func (t *ProgressTracker) remove(id uint64) {
	delete(t.Voters, id)
	delete(t.Learners, id)
	delete(t.Progress, id)
	delete(t.Votes, id)
}

func sortedIDs(set map[uint64]struct{}) []uint64 {
	ids := make([]uint64, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}
