package config

import "time"

// Config holds the bootstrap identity and Raft runtime configuration.
type Config struct {
	DBPath string

	// Peers is used only to bootstrap a new cluster/database. Once initialized,
	// the replicated ClusterMetadata registry is authoritative.
	StoreID   uint64
	ClusterID uint64
	Peers     map[uint64]string
	// Join starts a fresh local database without making this store a voter. The
	// caller must populate Peers and ClusterID from JoinInfo first; the member is
	// expected to have already been added as a learner.
	Join bool

	// Raft protocol timing and log retention.
	RaftElectionTimeoutTicks int
	RaftHeartbeatTicks       int
	RaftLogGcCountLimit      uint64

	// Raft tick interval.
	RaftBaseTickInterval time.Duration
}

// NewDefaultConfig returns a Config with sane raft defaults. Caller fills in
// DBPath, StoreID and Peers.
func NewDefaultConfig() *Config {
	return &Config{
		ClusterID:                1,
		RaftBaseTickInterval:     100 * time.Millisecond,
		RaftHeartbeatTicks:       2,
		RaftElectionTimeoutTicks: 10,
		RaftLogGcCountLimit:      128000,
	}
}
