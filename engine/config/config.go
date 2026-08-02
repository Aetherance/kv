package config

import "time"

// Config holds the engine and raftstore configuration.
type Config struct {
	DBPath string

	// Static cluster topology. StoreID identifies this store; Peers maps every
	// store id in the cluster to its gRPC address. There is no scheduler, so the
	// topology is fixed and shared by all nodes.
	StoreID uint64
	Peers   map[uint64]string

	// Raft related fields used by the raftstore.
	RaftElectionTimeoutTicks int
	RaftHeartbeatTicks       int
	RaftLogGcCountLimit      uint64

	// Tick intervals that drive the raftstore tickers.
	RaftBaseTickInterval  time.Duration
	RaftLogGCTickInterval time.Duration
}

// NewDefaultConfig returns a Config with sane raft defaults. Caller fills in
// DBPath, StoreID and Peers.
func NewDefaultConfig() *Config {
	return &Config{
		RaftBaseTickInterval:     100 * time.Millisecond,
		RaftHeartbeatTicks:       2,
		RaftElectionTimeoutTicks: 10,
		RaftLogGCTickInterval:    10 * time.Second,
		RaftLogGcCountLimit:      128000,
	}
}
