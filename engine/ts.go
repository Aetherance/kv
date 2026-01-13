package engine

type TimestampOracle interface {
	GetNextTs() uint64
}
