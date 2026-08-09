package config

import "time"

// Config holds the bootstrap identity and Raft runtime configuration.
type Config struct {
	DBPath string

	// Peers bootstraps a new database. Once initialized, the persisted
	// ClusterMetadata registry is authoritative for membership identity.
	StoreID   uint64
	ClusterID uint64
	Peers     map[uint64]string

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
