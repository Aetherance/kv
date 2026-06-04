package raft_cmdpb

import "google.golang.org/protobuf/proto"

func (m *RaftCmdRequest) Marshal() ([]byte, error)    { return proto.Marshal(m) }
func (m *RaftCmdRequest) Unmarshal(data []byte) error { return proto.Unmarshal(data, m) }
