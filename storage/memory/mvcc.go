package memory

type Version struct {
	CommitTs uint64
	Value []byte
	Deleted bool
}