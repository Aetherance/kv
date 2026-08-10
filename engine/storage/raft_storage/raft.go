package raft_storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/Aetherance/kv/proto/pkg/clusterpb"
	"github.com/Aetherance/kv/proto/pkg/raftpb"
	"github.com/Aetherance/kv/raft"
)

var errStopped = errors.New("raft storage: stopped")

// NotLeaderError reports that this store cannot accept a proposal.
type NotLeaderError struct {
	LeaderID uint64
}

func (e *NotLeaderError) Error() string {
	if e.LeaderID == 0 {
		return "raft storage: not leader"
	}
	return fmt.Sprintf("raft storage: not leader; leader=%d", e.LeaderID)
}

type raftOperation uint8

const (
	opStep raftOperation = iota
	opPropose
	opProposeConfChange
	opStatus
)

type raftEvent struct {
	op      raftOperation
	message *raftpb.Message
	data    []byte
	change  *raftpb.ConfChange
	status  *clusterStatus
	done    chan error
}

type clusterStatus struct {
	metadata    *clusterpb.ClusterMetadata
	confState   *raftpb.ConfState
	leaderID    uint64
	commitIndex uint64
	progress    map[uint64]raft.Progress
}

func (rs *RaftStorage) run(ctx context.Context) {
	err := rs.runLoop(ctx)
	rs.lifecycleMu.Lock()
	rs.runErr = err
	rs.lifecycleMu.Unlock()

	pendingErr := err
	if pendingErr == nil {
		pendingErr = errStopped
	}
	rs.failPending(pendingErr)
	close(rs.done)
}

func (rs *RaftStorage) runLoop(ctx context.Context) error {
	ticker := time.NewTicker(rs.config.RaftBaseTickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			rs.node.Tick()
			if err := rs.handleReady(); err != nil {
				return err
			}
		case event := <-rs.inbox:
			err, fatal := rs.handleEvent(event)
			event.done <- err
			if fatal {
				return err
			}
		}
	}
}

func (rs *RaftStorage) handleEvent(event raftEvent) (error, bool) {
	switch event.op {
	case opStep:
		if event.message == nil {
			return errors.New("raft storage: nil raft message"), false
		}
		if err := rs.node.Step(event.message); err != nil {
			return err, false
		}
	case opPropose:
		if rs.node.Raft.State != raft.StateLeader {
			return &NotLeaderError{LeaderID: rs.node.Raft.Lead}, false
		}
		if err := rs.node.Propose(event.data); err != nil {
			return err, false
		}
	case opProposeConfChange:
		if rs.node.Raft.State != raft.StateLeader {
			return &NotLeaderError{LeaderID: rs.node.Raft.Lead}, false
		}
		if event.change == nil {
			return errors.New("raft storage: nil conf change"), false
		}
		if err := rs.validateConfChange(event.change); err != nil {
			return err, false
		}
		if err := rs.node.ProposeConfChange(event.change); err != nil {
			return err, false
		}
	case opStatus:
		if event.status == nil {
			return errors.New("raft storage: nil status target"), false
		}
		event.status.metadata = cloneClusterMetadata(rs.state.cluster)
		event.status.confState = rs.node.ConfState()
		event.status.leaderID = rs.node.LeaderID()
		event.status.commitIndex = rs.node.CommitIndex()
		event.status.progress = rs.node.GetProgress()
		return nil, false
	default:
		return errors.New("raft storage: unknown operation"), false
	}

	err := rs.handleReady()
	return err, err != nil
}

// handleReady is the safety boundary between RawNode and durable storage.
func (rs *RaftStorage) handleReady() error {
	for rs.node.HasReady() {
		ready := rs.node.Ready()
		if err := rs.state.persist(&ready); err != nil {
			return fmt.Errorf("raft storage: persist ready: %w", err)
		}
		if !raft.IsEmptySnap(ready.Snapshot) {
			rs.transport.ReplaceMembers(clusterAddresses(rs.state.cluster))
		}

		for _, entry := range ready.CommittedEntries {
			switch entry.EntryType {
			case raftpb.EntryType_EntryNormal:
				if len(entry.Data) == 0 {
					if err := rs.state.markApplied(entry.Index); err != nil {
						return fmt.Errorf("raft storage: apply entry %d: %w", entry.Index, err)
					}
					continue
				}
				if err := rs.state.apply(entry.Index, entry.Data, rs.applyCommand); err != nil {
					return fmt.Errorf("raft storage: apply entry %d: %w", entry.Index, err)
				}
				rs.onApplied(entry.Data)

			case raftpb.EntryType_EntryConfChange:
				var change raftpb.ConfChange
				if err := proto.Unmarshal(entry.Data, &change); err != nil {
					return fmt.Errorf("raft storage: decode conf change at %d: %w", entry.Index, err)
				}
				var context clusterpb.ConfChangeContext
				if err := proto.Unmarshal(change.Context, &context); err != nil {
					return fmt.Errorf("raft storage: decode conf change context at %d: %w", entry.Index, err)
				}
				nextCluster, err := applyClusterChange(rs.state.cluster, &change, &context)
				if err != nil {
					return fmt.Errorf("raft storage: update cluster metadata at %d: %w", entry.Index, err)
				}
				state := rs.node.ApplyConfChange(&change)
				if err := rs.state.applyMembership(entry.Index, state, nextCluster); err != nil {
					return fmt.Errorf("raft storage: apply conf change at %d: %w", entry.Index, err)
				}
				rs.transport.ReplaceMembers(clusterAddresses(nextCluster))
				if context.ProposerId == rs.config.StoreID {
					rs.completePending(context.Sequence, nil)
				}
				if change.ChangeType == raftpb.ConfChangeType_AddLearnerNode {
					if err := rs.state.forceSnapshot(); err != nil {
						return fmt.Errorf("raft storage: snapshot after learner add: %w", err)
					}
				}

			default:
				return fmt.Errorf("raft storage: unknown entry type %s", entry.EntryType)
			}
		}

		if len(ready.Messages) > 0 {
			rs.sendMessages(ready.Messages)
		}
		if err := rs.state.maybeCompact(rs.config.RaftLogGcCountLimit); err != nil {
			return fmt.Errorf("raft storage: compact: %w", err)
		}

		if ready.SoftState != nil && ready.SoftState.RaftState != raft.StateLeader {
			rs.failPending(errLeadershipLost)
		}
		rs.node.Advance(&ready)
	}
	return nil
}

func (rs *RaftStorage) step(ctx context.Context, message *raftpb.Message) error {
	return rs.submit(ctx, raftEvent{op: opStep, message: message})
}

func (rs *RaftStorage) proposeData(ctx context.Context, data []byte) error {
	return rs.submit(ctx, raftEvent{op: opPropose, data: append([]byte(nil), data...)})
}

func (rs *RaftStorage) proposeConfChangeData(ctx context.Context, change *raftpb.ConfChange) error {
	return rs.submit(ctx, raftEvent{op: opProposeConfChange, change: change})
}

func (rs *RaftStorage) status(ctx context.Context) (*clusterStatus, error) {
	status := new(clusterStatus)
	if err := rs.submit(ctx, raftEvent{op: opStatus, status: status}); err != nil {
		return nil, err
	}
	return status, nil
}

func (rs *RaftStorage) submit(ctx context.Context, event raftEvent) error {
	if rs.inbox == nil || rs.done == nil {
		return errStopped
	}
	event.done = make(chan error, 1)
	select {
	case rs.inbox <- event:
	case <-rs.done:
		return rs.stoppedError()
	case <-ctx.Done():
		return ctx.Err()
	}

	select {
	case err := <-event.done:
		return err
	case <-rs.done:
		return rs.stoppedError()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (rs *RaftStorage) stoppedError() error {
	rs.lifecycleMu.Lock()
	defer rs.lifecycleMu.Unlock()
	if rs.runErr != nil {
		return rs.runErr
	}
	return errStopped
}
