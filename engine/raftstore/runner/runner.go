package runner

import (
	"github.com/Aetherance/kv/engine/raftstore/message"
	"github.com/Aetherance/kv/proto/pkg/metapb"
)

type SplitCheckTask struct {
	Region *metapb.Region
}

type SchedulerAskSplitTask struct {
	Region   *metapb.Region
	SplitKey []byte
	Peer     *metapb.Peer
	Callback *message.Callback
}

type SchedulerRegionHeartbeatTask struct {
	Region          *metapb.Region
	Peer            *metapb.Peer
	PendingPeers    []*metapb.Peer
	ApproximateSize *uint64
}
