package util

import (
	"fmt"

	"github.com/Aetherance/kv/log"
	"github.com/Aetherance/kv/proto/pkg/metapb"
	"github.com/Aetherance/kv/proto/pkg/raft_cmdpb"
	eraftpb "github.com/Aetherance/kv/proto/pkg/raftpb"
)

const RaftInvalidIndex uint64 = 0
const InvalidID uint64 = 0

// IsInitialMsg checks whether the `msg` can be used to initialize a new peer or not.
// There could be two cases:
//  1. Target peer already exists but has not established communication with leader yet
//  2. Target peer is added newly due to member change or region split, but it's not
//     created yet
//
// For both cases the region start key and end key are attached in RequestVote and
// Heartbeat message for the store of that peer to check whether to create a new peer
// when receiving these messages, or just to wait for a pending region split to perform
// later.
func IsInitialMsg(msg *eraftpb.Message) bool {
	return msg.MsgType == eraftpb.MessageType_MsgRequestVote ||
		// the peer has not been known to this leader, it may exist or not.
		(msg.MsgType == eraftpb.MessageType_MsgHeartbeat && msg.Commit == RaftInvalidIndex)
}

// IsEpochStale checks whether epoch is staler than check_epoch.
func IsEpochStale(epoch *metapb.RegionEpoch, checkEpoch *metapb.RegionEpoch) bool {
	return epoch.Version < checkEpoch.Version || epoch.ConfVer < checkEpoch.ConfVer
}

func IsVoteMessage(msg *eraftpb.Message) bool {
	tp := msg.GetMsgType()
	return tp == eraftpb.MessageType_MsgRequestVote
}

func CheckRegionEpoch(req *raft_cmdpb.RaftCmdRequest, region *metapb.Region, includeRegion bool) error {
	if req.AdminRequest != nil {
		return nil
	}

	if req.Header == nil {
		return fmt.Errorf("missing header!")
	}

	if req.Header.RegionEpoch == nil {
		return fmt.Errorf("missing epoch!")
	}

	fromEpoch := req.Header.RegionEpoch
	currentEpoch := region.RegionEpoch

	// We must check epochs strictly to avoid key not in region error.
	if fromEpoch.Version != currentEpoch.Version {
		log.Debugf("epoch not match, region id %v, from epoch %v, current epoch %v",
			region.Id, fromEpoch, currentEpoch)

		regions := []*metapb.Region{}
		if includeRegion {
			regions = []*metapb.Region{region}
		}
		return &ErrEpochNotMatch{Message: fmt.Sprintf("current epoch of region %v is %v, but you sent %v",
			region.Id, currentEpoch, fromEpoch), Regions: regions}
	}

	return nil
}

func FindPeer(region *metapb.Region, storeID uint64) *metapb.Peer {
	for _, peer := range region.Peers {
		if peer.StoreId == storeID {
			return peer
		}
	}
	return nil
}

func ConfStateFromRegion(region *metapb.Region) (confState eraftpb.ConfState) {
	for _, p := range region.Peers {
		confState.Nodes = append(confState.Nodes, p.GetId())
	}
	return
}

func CheckStoreID(req *raft_cmdpb.RaftCmdRequest, storeID uint64) error {
	peer := req.Header.Peer
	if peer.StoreId == storeID {
		return nil
	}
	return fmt.Errorf("store not match %d %d", peer.StoreId, storeID)
}

func CheckTerm(req *raft_cmdpb.RaftCmdRequest, term uint64) error {
	header := req.Header
	if header.Term == 0 || term <= header.Term+1 {
		return nil
	}
	// If header's term is 2 verions behind current term,
	// leadership may have been changed away.
	return &ErrStaleCommand{}
}

func CheckPeerID(req *raft_cmdpb.RaftCmdRequest, peerID uint64) error {
	peer := req.Header.Peer
	if peer.Id == peerID {
		return nil
	}
	return fmt.Errorf("mismatch peer id %d != %d", peer.Id, peerID)
}

func PeerEqual(l, r *metapb.Peer) bool {
	return l.Id == r.Id && l.StoreId == r.StoreId
}

func RegionEqual(l, r *metapb.Region) bool {
	if l == nil || r == nil {
		return false
	}
	return l.Id == r.Id && l.RegionEpoch.Version == r.RegionEpoch.Version && l.RegionEpoch.ConfVer == r.RegionEpoch.ConfVer
}
