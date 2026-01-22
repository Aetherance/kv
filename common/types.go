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
	OpPing
	OpCommand
)

type Request struct {
	Op    OpType
	Key   []byte
	Value []byte
}
