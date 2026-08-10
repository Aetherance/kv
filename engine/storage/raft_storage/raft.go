package raft_storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"

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
	opReadIndex
)

type raftEvent struct {
	op      raftOperation
	message *raftpb.Message
	data    []byte
	done    chan error
}

func (rs *RaftStorage) run(ctx context.Context) {
	err := rs.runLoop(ctx)
	rs.runErr = err

	pendingErr := err
	if pendingErr == nil {
		pendingErr = errStopped
	}
	rs.failPending(pendingErr)
	rs.failPendingReads(pendingErr)
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
	case opReadIndex:
		leaderID := rs.node.Raft.Lead
		if rs.node.Raft.State == raft.StateLeader {
			leaderID = rs.config.StoreID
		}
		if leaderID == raft.None {
			return &NotLeaderError{}, false
		}
		if !rs.setPendingReadLeader(event.data, leaderID) {
			return nil, false
		}
		if err := rs.node.ReadIndex(event.data); err != nil {
			return err, false
		}
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
				state := rs.node.ApplyConfChange(&change)
				if err := rs.state.applyConfChange(entry.Index, state); err != nil {
					return fmt.Errorf("raft storage: apply conf change at %d: %w", entry.Index, err)
				}

			default:
				return fmt.Errorf("raft storage: unknown entry type %s", entry.EntryType)
			}
		}
		for _, readState := range ready.ReadStates {
			rs.onReadState(readState)
		}
		rs.completeAppliedReads()

		if len(ready.Messages) > 0 {
			rs.sendMessages(ready.Messages)
		}
		if err := rs.state.maybeCompact(rs.config.RaftLogGcCountLimit); err != nil {
			return fmt.Errorf("raft storage: compact: %w", err)
		}

		if ready.SoftState != nil && ready.SoftState.RaftState != raft.StateLeader {
			rs.failPending(errLeadershipLost)
		}
		if ready.SoftState != nil {
			activeLeader := ready.SoftState.Lead
			if ready.SoftState.RaftState == raft.StateLeader {
				activeLeader = rs.config.StoreID
			}
			rs.failReadsForLeaderChange(activeLeader)
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

func (rs *RaftStorage) readIndexData(ctx context.Context, data []byte) error {
	return rs.submit(ctx, raftEvent{op: opReadIndex, data: append([]byte(nil), data...)})
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
	if rs.runErr != nil {
		return rs.runErr
	}
	return errStopped
}
