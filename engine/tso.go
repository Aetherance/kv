package engine

import "sync"

type TimestampOracle interface {
	GetNextTs() uint64
}

type Tso struct {
	mu sync.Mutex
	ts uint64
}

func NewTSO() *Tso {
	return &Tso{}
}

func (t *Tso) GetNextTs() uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ts++
	return t.ts
}
