package runner

import (
	"github.com/Aetherance/kv/engine/raftstore/meta"
	engine_util "github.com/Aetherance/kv/engine/util"
	"github.com/Aetherance/kv/engine/util/worker"
	"github.com/Aetherance/kv/log"
	badger "github.com/dgraph-io/badger/v4"
)

type RaftLogGCTask struct {
	RaftEngine *badger.DB
	RegionID   uint64
	StartIdx   uint64
	EndIdx     uint64
}

type raftLogGCTaskHandler struct{}

func NewRaftLogGCTaskHandler() *raftLogGCTaskHandler {
	return &raftLogGCTaskHandler{}
}

// gcRaftLog does the GC job and returns the count of logs collected.
func (r *raftLogGCTaskHandler) gcRaftLog(raftDb *badger.DB, regionId, startIdx, endIdx uint64) (uint64, error) {
	// Find the raft log idx range needed to be gc.
	firstIdx := startIdx
	if firstIdx == 0 {
		firstIdx = endIdx
		err := raftDb.View(func(txn *badger.Txn) error {
			startKey := meta.RaftLogKey(regionId, 0)
			ite := txn.NewIterator(badger.DefaultIteratorOptions)
			defer ite.Close()
			if ite.Seek(startKey); ite.Valid() {
				var err error
				if firstIdx, err = meta.RaftLogIndex(ite.Item().Key()); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return 0, err
		}
	}

	if firstIdx >= endIdx {
		log.Infof("no need to gc, [regionId: %d]", regionId)
		return 0, nil
	}

	raftWb := engine_util.WriteBatch{}
	for idx := firstIdx; idx < endIdx; idx += 1 {
		key := meta.RaftLogKey(regionId, idx)
		raftWb.DeleteMeta(key)
	}
	if raftWb.Len() != 0 {
		if err := raftWb.WriteToDB(raftDb); err != nil {
			return 0, err
		}
	}
	return endIdx - firstIdx, nil
}

func (r *raftLogGCTaskHandler) Handle(t worker.Task) {
	logGcTask, ok := t.(*RaftLogGCTask)
	if !ok {
		log.Errorf("unsupported worker.Task: %+v", t)
		return
	}
	log.Debugf("execute gc log. [regionId: %d, endIndex: %d]", logGcTask.RegionID, logGcTask.EndIdx)
	collected, err := r.gcRaftLog(logGcTask.RaftEngine, logGcTask.RegionID, logGcTask.StartIdx, logGcTask.EndIdx)
	if err != nil {
		log.Errorf("failed to gc. [regionId: %d, collected: %d, err: %v]", logGcTask.RegionID, collected, err)
	} else {
		log.Debugf("collected log entries. [regionId: %d, entryCount: %d]", logGcTask.RegionID, collected)
	}
}
