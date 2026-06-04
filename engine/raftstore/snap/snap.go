package snap

import (
	"os"

	eraftpb "github.com/Aetherance/kv/proto/pkg/raftpb"
)

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

// SnapKey identifies a snapshot by region/term/index.
type SnapKey struct {
	RegionID uint64
	Term     uint64
	Index    uint64
}

type SnapKeyWithSending struct {
	SnapKey   SnapKey
	IsSending bool
}

// SnapKeyFromRegionSnap builds a SnapKey. Stub fills only the region id.
func SnapKeyFromRegionSnap(regionID uint64, snapshot *eraftpb.Snapshot) SnapKey {
	return SnapKey{RegionID: regionID}
}

// Snapshot is a handle to an on-disk snapshot. Stub interface.
type Snapshot interface {
	Meta() (os.FileInfo, error)
}

// SnapManager manages snapshot files. All methods are stubs.
type SnapManager struct{}

func (m *SnapManager) GetSnapshotForApplying(key SnapKey) (Snapshot, error) { return nil, nil }

func (m *SnapManager) GetSnapshotForSending(key SnapKey) (Snapshot, error) { return nil, nil }

func (m *SnapManager) DeleteSnapshot(key SnapKey, snapshot Snapshot, checkExists bool) bool {
	return true
}
