package runner

import (
	"encoding/hex"
	"fmt"

	"github.com/Aetherance/kv/engine/raftstore/meta"
	"github.com/Aetherance/kv/engine/raftstore/util"
	engine_util "github.com/Aetherance/kv/engine/util"
	"github.com/Aetherance/kv/engine/util/worker"
	"github.com/Aetherance/kv/log"
	rspb "github.com/Aetherance/kv/proto/pkg/raft_serverpb"
	eraftpb "github.com/Aetherance/kv/proto/pkg/raftpb"
	badger "github.com/dgraph-io/badger/v4"
)

// There're some tasks for region worker, such as:
// `RegionTaskGen` which will cause the worker to generate a snapshot according to RegionId,
// `RegionTaskApply` which will apply a snapshot to the region that id equals RegionId,
// `RegionTaskDestroy` which will clean up the key range from StartKey to EndKey.
//
// Unlike upstream TinyKV which stores snapshots as SST files, this implementation
// serializes the region's key/value data directly into RaftSnapshotData (proto),
// which is friendly to the standard badger v4.

type RegionTaskGen struct {
	RegionId uint64                   // specify the region which the task is for.
	Notifier chan<- *eraftpb.Snapshot // when it finishes snapshot generating, it notifies notifier.
}

type RegionTaskApply struct {
	RegionId uint64                    // specify the region which the task is for.
	Notifier chan<- bool               // when it finishes snapshot applying, it notifies notifier.
	SnapMeta *eraftpb.SnapshotMetadata // the snapshot meta information
	SnapData *rspb.RaftSnapshotData    // the snapshot data (region kv pairs) to write
	StartKey []byte                    // origin region's range, used to clean up before applying.
	EndKey   []byte
}

type RegionTaskDestroy struct {
	RegionId uint64 // specify the region which the task is for.
	StartKey []byte // `StartKey` and `EndKey` are used to destroy certain range of region.
	EndKey   []byte
}

type regionTaskHandler struct {
	engines *engine_util.Engines
}

func NewRegionTaskHandler(engines *engine_util.Engines) *regionTaskHandler {
	return &regionTaskHandler{engines: engines}
}

func (r *regionTaskHandler) Handle(t worker.Task) {
	switch t.(type) {
	case *RegionTaskGen:
		task := t.(*RegionTaskGen)
		r.handleGen(task.RegionId, task.Notifier)
	case *RegionTaskApply:
		task := t.(*RegionTaskApply)
		r.handleApply(task)
	case *RegionTaskDestroy:
		task := t.(*RegionTaskDestroy)
		r.cleanUpRange(task.RegionId, task.StartKey, task.EndKey)
	}
}

// handleGen generates a snapshot of the Region and notifies the caller.
func (r *regionTaskHandler) handleGen(regionId uint64, notifier chan<- *eraftpb.Snapshot) {
	snapshot, err := doSnapshot(r.engines, regionId)
	if err != nil {
		log.Errorf("failed to generate snapshot!!!, [regionId: %d, err : %v]", regionId, err)
		notifier <- nil
	} else {
		notifier <- snapshot
	}
}

// handleApply applies the snapshot of the specified Region.
func (r *regionTaskHandler) handleApply(task *RegionTaskApply) {
	err := r.applySnap(task)
	if err != nil {
		task.Notifier <- false
		log.Fatalf("failed to apply snap!!!. err: %v", err)
		return
	}
	task.Notifier <- true
}

func (r *regionTaskHandler) applySnap(task *RegionTaskApply) error {
	log.Infof("begin apply snap data. [regionId: %d]", task.RegionId)
	// clear up the region data before applying snapshot.
	r.cleanUpRange(task.RegionId, task.StartKey, task.EndKey)
	if task.SnapData == nil {
		return nil
	}
	// write the snapshot key/value pairs back. Keys already carry their CF prefix.
	return r.engines.Kv.Update(func(txn *badger.Txn) error {
		for _, kv := range task.SnapData.Data {
			if err := txn.Set(kv.Key, kv.Value); err != nil {
				return err
			}
		}
		return nil
	})
}

// cleanUpRange cleans up the data within the range.
func (r *regionTaskHandler) cleanUpRange(regionId uint64, startKey, endKey []byte) {
	if err := engine_util.DeleteRange(r.engines.Kv, startKey, endKey); err != nil {
		log.Fatalf("failed to delete data in range, [regionId: %d, startKey: %s, endKey: %s, err: %v]", regionId,
			hex.EncodeToString(startKey), hex.EncodeToString(endKey), err)
	} else {
		log.Infof("succeed in deleting data in range. [regionId: %d, startKey: %s, endKey: %s]", regionId,
			hex.EncodeToString(startKey), hex.EncodeToString(endKey))
	}
}

func getAppliedIdxTermForSnapshot(raft *badger.DB, kv *badger.Txn, regionId uint64) (uint64, uint64, error) {
	applyState := new(rspb.RaftApplyState)
	err := engine_util.GetMetaFromTxn(kv, meta.ApplyStateKey(regionId), applyState)
	if err != nil {
		return 0, 0, err
	}

	idx := applyState.AppliedIndex
	var term uint64
	if idx == applyState.TruncatedState.Index {
		term = applyState.TruncatedState.Term
	} else {
		entry, err := meta.GetRaftEntry(raft, regionId, idx)
		if err != nil {
			return 0, 0, err
		} else {
			term = entry.GetTerm()
		}
	}
	return idx, term, nil
}

// doSnapshot builds a snapshot by serializing the region's CF data into RaftSnapshotData.
func doSnapshot(engines *engine_util.Engines, regionId uint64) (*eraftpb.Snapshot, error) {
	log.Debugf("begin to generate a snapshot. [regionId: %d]", regionId)

	txn := engines.Kv.NewTransaction(false)
	defer txn.Discard()

	index, term, err := getAppliedIdxTermForSnapshot(engines.Raft, txn, regionId)
	if err != nil {
		return nil, err
	}

	regionState := new(rspb.RegionLocalState)
	if err := engine_util.GetMetaFromTxn(txn, meta.RegionStateKey(regionId), regionState); err != nil {
		return nil, err
	}
	if regionState.GetState() != rspb.PeerState_Normal {
		return nil, fmt.Errorf("snap job %d seems stale, skip", regionId)
	}

	region := regionState.GetRegion()
	confState := util.ConfStateFromRegion(region)

	snapData := &rspb.RaftSnapshotData{Region: region}
	for _, cf := range engine_util.CFs {
		it := engine_util.NewCFIterator(cf, txn)
		for it.Seek(region.StartKey); it.Valid(); it.Next() {
			item := it.Item()
			key := item.KeyCopy(nil)
			if engine_util.ExceedEndKey(key, region.EndKey) {
				break
			}
			val, err := item.ValueCopy(nil)
			if err != nil {
				it.Close()
				return nil, err
			}
			snapData.Data = append(snapData.Data, &rspb.KeyValue{
				Key:   engine_util.KeyWithCF(cf, key),
				Value: val,
			})
		}
		it.Close()
	}

	snapshot := &eraftpb.Snapshot{
		Metadata: &eraftpb.SnapshotMetadata{
			Index:     index,
			Term:      term,
			ConfState: &confState,
		},
	}
	snapshot.Data, err = snapData.Marshal()
	return snapshot, err
}
