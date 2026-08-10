package raft

import pb "github.com/Aetherance/kv/proto/pkg/raftpb"

type readIndexStatus struct {
	request *pb.Message
	index   uint64
	acks    map[uint64]struct{}
}

// readOnly tracks safe ReadIndex requests in arrival order. A quorum
// acknowledgement for a later heartbeat also confirms every earlier request.
type readOnly struct {
	pending map[string]*readIndexStatus
	queue   []string
}

func newReadOnly() *readOnly {
	return &readOnly{pending: make(map[string]*readIndexStatus)}
}

func (ro *readOnly) addRequest(index uint64, request *pb.Message, self uint64) bool {
	if request == nil || len(request.Context) == 0 {
		return false
	}
	key := string(request.Context)
	if _, ok := ro.pending[key]; ok {
		return false
	}
	requestCopy := *request
	requestCopy.Context = append([]byte(nil), request.Context...)
	ro.pending[key] = &readIndexStatus{
		request: &requestCopy,
		index:   index,
		acks:    map[uint64]struct{}{self: {}},
	}
	ro.queue = append(ro.queue, key)
	return true
}

func (ro *readOnly) recvAck(from uint64, context []byte) int {
	status := ro.pending[string(context)]
	if status == nil {
		return 0
	}
	status.acks[from] = struct{}{}
	return len(status.acks)
}

func (ro *readOnly) advance(context []byte) []*readIndexStatus {
	key := string(context)
	position := -1
	for i, queued := range ro.queue {
		if queued == key {
			position = i
			break
		}
	}
	if position < 0 {
		return nil
	}

	completed := make([]*readIndexStatus, 0, position+1)
	for _, queued := range ro.queue[:position+1] {
		if status := ro.pending[queued]; status != nil {
			completed = append(completed, status)
			delete(ro.pending, queued)
		}
	}
	ro.queue = append([]string(nil), ro.queue[position+1:]...)
	return completed
}

func (ro *readOnly) reset() {
	ro.pending = make(map[string]*readIndexStatus)
	ro.queue = nil
}
