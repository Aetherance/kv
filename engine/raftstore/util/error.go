package util

import (
	"fmt"

	"github.com/Aetherance/kv/proto/pkg/errorpb"
	"github.com/Aetherance/kv/proto/pkg/metapb"
)

type ErrNotLeader struct {
	RegionId uint64
	Leader   *metapb.Peer
}

func (e *ErrNotLeader) Error() string {
	return fmt.Sprintf("region %v is not leader", e.RegionId)
}

type ErrRegionNotFound struct {
	RegionId uint64
}

func (e *ErrRegionNotFound) Error() string {
	return fmt.Sprintf("region %v is not found", e.RegionId)
}

type ErrEpochNotMatch struct {
	Message string
	Regions []*metapb.Region
}

func (e *ErrEpochNotMatch) Error() string {
	return fmt.Sprintf("epoch not match, error msg %v, regions %v", e.Message, e.Regions)
}

type ErrStaleCommand struct{}

func (e *ErrStaleCommand) Error() string {
	return fmt.Sprintf("stale command")
}

func RaftstoreErrToPbError(e error) *errorpb.Error {
	ret := new(errorpb.Error)
	switch err := e.(type) {
	case *ErrNotLeader:
		ret.NotLeader = &errorpb.NotLeader{RegionId: err.RegionId, Leader: err.Leader}
	case *ErrRegionNotFound:
		ret.RegionNotFound = &errorpb.RegionNotFound{RegionId: err.RegionId}
	case *ErrEpochNotMatch:
		ret.EpochNotMatch = &errorpb.EpochNotMatch{CurrentRegions: err.Regions}
	case *ErrStaleCommand:
		ret.StaleCommand = &errorpb.StaleCommand{}
	default:
		ret.Message = e.Error()
	}
	return ret
}
