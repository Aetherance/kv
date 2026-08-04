package raftruntime

import (
	"fmt"

	"google.golang.org/protobuf/proto"

	"github.com/Aetherance/kv/proto/pkg/raftpb"
	"github.com/Aetherance/kv/raft"
)

// Runtime serially connects a RawNode to durable storage, a state machine, and
// an outbound message sink. Runtime is intentionally not thread-safe; callers
// choose the scheduling model and must serialize all methods.
type Runtime struct {
	node   *raft.RawNode
	engine *BadgerEngine
	apply  ApplyFunc
	config Config
}

func New(node *raft.RawNode, engine *BadgerEngine, apply ApplyFunc, config Config) *Runtime {
	return &Runtime{node: node, engine: engine, apply: apply, config: config}
}

func (d *Runtime) Tick() error {
	d.node.Tick()
	return d.Drive()
}

func (d *Runtime) Campaign() error {
	if err := d.node.Campaign(); err != nil {
		return err
	}
	return d.Drive()
}

func (d *Runtime) Step(message *raftpb.Message) error {
	if message == nil {
		return fmt.Errorf("raftruntime: nil raft message")
	}
	if err := d.node.Step(message); err != nil {
		return err
	}
	return d.Drive()
}

// Propose only means that the local leader accepted the proposal. The caller
// learns durability through AppliedFunc and owns request correlation.
func (d *Runtime) Propose(data []byte) error {
	if d.node.Raft.State != raft.StateLeader {
		return &NotLeaderError{LeaderID: d.node.Raft.Lead}
	}
	if err := d.node.Propose(data); err != nil {
		return err
	}
	return d.Drive()
}

func (d *Runtime) ProposeConfChange(change *raftpb.ConfChange) error {
	if change == nil {
		return fmt.Errorf("raftruntime: nil configuration change")
	}
	if d.node.Raft.State != raft.StateLeader {
		return &NotLeaderError{LeaderID: d.node.Raft.Lead}
	}
	if err := d.node.ProposeConfChange(change); err != nil {
		return err
	}
	return d.Drive()
}

// Drive consumes all currently available Ready batches. Its ordering is the
// central safety contract of this package: persist first, atomically apply the
// state machine and applied index, enqueue messages, then Advance.
func (d *Runtime) Drive() error {
	for d.node.HasReady() {
		ready := d.node.Ready()
		if err := d.engine.Persist(&ready); err != nil {
			return &FatalError{Err: fmt.Errorf("raftruntime: persist ready: %w", err)}
		}

		applied := make([]Applied, 0, len(ready.CommittedEntries))
		for _, entry := range ready.CommittedEntries {
			result, emit, err := d.applyEntry(entry)
			if err != nil {
				return &FatalError{Err: fmt.Errorf("raftruntime: apply entry %d: %w", entry.Index, err)}
			}
			if emit {
				applied = append(applied, Applied{
					Index:  entry.Index,
					Type:   entry.EntryType,
					Data:   append([]byte(nil), entry.Data...),
					Result: result,
				})
			}
		}

		if d.config.Send != nil && len(ready.Messages) > 0 {
			d.config.Send(ready.Messages)
		}

		if d.config.CompactThreshold > 0 {
			if err := d.engine.MaybeCompact(d.config.CompactThreshold); err != nil {
				return &FatalError{Err: fmt.Errorf("raftruntime: compact: %w", err)}
			}
		}

		d.node.Advance(&ready)

		if ready.SoftState != nil && d.config.StateChanged != nil {
			d.config.StateChanged(*ready.SoftState)
		}
		if len(applied) > 0 && d.config.Applied != nil {
			d.config.Applied(applied)
		}
	}
	return nil
}

func (d *Runtime) applyEntry(entry *raftpb.Entry) ([]byte, bool, error) {
	switch entry.EntryType {
	case raftpb.EntryType_EntryNormal:
		if len(entry.Data) == 0 {
			return nil, false, d.engine.MarkApplied(entry.Index)
		}
		if d.apply == nil {
			return nil, false, fmt.Errorf("normal entry has no state-machine apply function")
		}
		result, err := d.engine.Apply(entry.Index, entry.Data, d.apply)
		return result, true, err

	case raftpb.EntryType_EntryConfChange:
		var change raftpb.ConfChange
		if err := proto.Unmarshal(entry.Data, &change); err != nil {
			return nil, false, err
		}
		state := d.node.ApplyConfChange(&change)
		if err := d.engine.ApplyConfChange(entry.Index, state); err != nil {
			return nil, false, err
		}
		return nil, true, nil

	default:
		return nil, false, fmt.Errorf("unknown entry type %s", entry.EntryType)
	}
}
