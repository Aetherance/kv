package raftruntime

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/Aetherance/kv/proto/pkg/raftpb"
)

var ErrStopped = errors.New("raftruntime: runner stopped")

type operation uint8

const (
	opStep operation = iota
	opPropose
	opConfChange
	opCampaign
)

type event struct {
	op      operation
	message *raftpb.Message
	data    []byte
	change  *raftpb.ConfChange
	done    chan error
}

// Runner is an optional one-goroutine scheduler for a Runtime. A future
// multi-instance scheduler can call Runtime directly without changing it.
type Runner struct {
	runtime      *Runtime
	tickInterval time.Duration
	inbox        chan event
	done         chan struct{}

	errMu sync.RWMutex
	err   error
}

func NewRunner(runtime *Runtime, tickInterval time.Duration) *Runner {
	return &Runner{
		runtime:      runtime,
		tickInterval: tickInterval,
		inbox:        make(chan event, 256),
		done:         make(chan struct{}),
	}
}

func (r *Runner) Run(ctx context.Context) error {
	defer close(r.done)
	if r.tickInterval <= 0 {
		return r.finish(errors.New("raftruntime: tick interval must be positive"))
	}
	ticker := time.NewTicker(r.tickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return r.finish(nil)
		case <-ticker.C:
			if err := r.runtime.Tick(); err != nil {
				return r.finish(err)
			}
		case ev := <-r.inbox:
			err := r.handle(ev)
			ev.done <- err
			if IsFatal(err) {
				return r.finish(err)
			}
		}
	}
}

func (r *Runner) Step(ctx context.Context, message *raftpb.Message) error {
	return r.submit(ctx, event{op: opStep, message: message})
}

func (r *Runner) Propose(ctx context.Context, data []byte) error {
	return r.submit(ctx, event{op: opPropose, data: append([]byte(nil), data...)})
}

func (r *Runner) ProposeConfChange(ctx context.Context, change *raftpb.ConfChange) error {
	return r.submit(ctx, event{op: opConfChange, change: change})
}

func (r *Runner) Campaign(ctx context.Context) error {
	return r.submit(ctx, event{op: opCampaign})
}

func (r *Runner) Done() <-chan struct{} { return r.done }

func (r *Runner) Err() error {
	r.errMu.RLock()
	defer r.errMu.RUnlock()
	return r.err
}

func (r *Runner) submit(ctx context.Context, ev event) error {
	ev.done = make(chan error, 1)
	select {
	case r.inbox <- ev:
	case <-r.done:
		return r.stoppedError()
	case <-ctx.Done():
		return ctx.Err()
	}

	select {
	case err := <-ev.done:
		return err
	case <-r.done:
		return r.stoppedError()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Runner) handle(ev event) error {
	switch ev.op {
	case opStep:
		return r.runtime.Step(ev.message)
	case opPropose:
		return r.runtime.Propose(ev.data)
	case opConfChange:
		return r.runtime.ProposeConfChange(ev.change)
	case opCampaign:
		return r.runtime.Campaign()
	default:
		return errors.New("raftruntime: unknown runner operation")
	}
}

func (r *Runner) finish(err error) error {
	r.errMu.Lock()
	r.err = err
	r.errMu.Unlock()
	return err
}

func (r *Runner) stoppedError() error {
	if err := r.Err(); err != nil {
		return err
	}
	return ErrStopped
}
