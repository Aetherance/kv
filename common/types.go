package common

type Response struct {
	Data interface{}
	Err  error
}

type OpType int

const (
	OpUnknown OpType = iota
	OpGet
	OpSet
	OpDel
)

type Request struct {
	Op    OpType
	Key   []byte
	Value []byte
}
