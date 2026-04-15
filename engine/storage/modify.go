package storage

// Modify wraps a future storage mutation such as Put/Delete.
type Modify struct {
	Data interface{}
}
