package message

import (
	"github.com/Aetherance/kv/proto/pkg/raft_cmdpb"
)

type MsgType int64

const (
	// just a placeholder
	MsgTypeNull MsgType = 0
	// message to start the ticker of peer
	MsgTypeStart MsgType = 1
	// message of base tick to drive the ticker
	MsgTypeTick MsgType = 2
	// message wraps a raft message that should be forwarded to Raft module
	// the raft message is from peer on other store
	MsgTypeRaftMessage MsgType = 3
	// message wraps a raft command that maybe a read/write request or admin request
	// the raft command should be proposed to Raft module
	MsgTypeRaftCmd MsgType = 4
	// message wraps a raft message to the peer not existing on the Store.
	// It is due to region split or add peer conf change
	MsgTypeStoreRaftMessage MsgType = 101
	// message to start the ticker of store
	MsgTypeStoreStart MsgType = 107
)

type Msg struct {
	Type     MsgType
	RegionID uint64
	Data     interface{}
}

func NewMsg(tp MsgType, data interface{}) Msg {
	return Msg{Type: tp, Data: data}
}

func NewPeerMsg(tp MsgType, regionID uint64, data interface{}) Msg {
	return Msg{Type: tp, RegionID: regionID, Data: data}
}

type MsgRaftCmd struct {
	Request  *raft_cmdpb.RaftCmdRequest
	Callback *Callback
}
