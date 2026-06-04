package raft_serverpb

import "google.golang.org/protobuf/proto"

func (m *RaftSnapshotData) Marshal() ([]byte, error)    { return proto.Marshal(m) }
func (m *RaftSnapshotData) Unmarshal(data []byte) error { return proto.Unmarshal(data, m) }
