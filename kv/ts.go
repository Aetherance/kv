package kv

type TimestampOracle interface {
	GetNextTs() uint64
}