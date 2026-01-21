package protocol

import (
	"bufio"
	"context"

	"github.com/Aetherance/kv/common"
	"github.com/Aetherance/kv/coord"
)

type Protocol interface {
	// This method supports the parse of request
	ParseRequest(reader *bufio.Reader) (*common.Request, error)

	// This method encode response to bytes
	EncodeResponse(resp common.Response) []byte
}

type Exporter interface {
	Export(ctx context.Context, c coord.Coordinator, addr string) error

	Stop(ctx context.Context) error
}
