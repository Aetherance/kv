package storage

// Modify wraps a future storage mutation such as Put/Delete.
type Modify struct {
	Data interface{}
}

type Put struct {
	Cf  string
	Key []byte
	Val []byte
}

type Delete struct {
	Cf  string
	Key []byte
}
