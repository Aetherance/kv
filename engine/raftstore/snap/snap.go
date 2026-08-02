package snap

import eraftpb "github.com/Aetherance/kv/proto/pkg/raftpb"

// SnapStateType enumerates the snapshot lifecycle states. Stub values.
type SnapStateType int

const (
	SnapState_Relax      SnapStateType = 0
	SnapState_Generating SnapStateType = 1
	SnapState_Applying   SnapStateType = 2
)

type SnapState struct {
	StateType SnapStateType
	Receiver  chan *eraftpb.Snapshot
}
