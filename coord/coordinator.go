package coord

import (
	"context"

	"github.com/Aetherance/kv/common"
)

type Coordinator interface {
	Coordinator(ctx context.Context, req *common.Request) *common.Response
}
