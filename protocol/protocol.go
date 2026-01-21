package protocol

import (
	"context"
	"net"

	"github.com/Aetherance/kv/common"
)

type Protocol interface {
	// This method supports the parse of request
	ParseRequest(conn net.Conn) (*common.Request, error)

	// This method encode response to bytes
	EncodeResponse(resp common.Response) []byte
}

type Exporter interface {
	Export(ctx context.Context)
}
