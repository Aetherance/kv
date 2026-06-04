package raftpb

import "google.golang.org/protobuf/proto"

// Convenience Marshal/Unmarshal methods so callers can use the gogo-style
// method form (e.g. entry.Unmarshal(data)) on top of the google.golang.org
// generated messages.

func (m *Entry) Marshal() ([]byte, error)    { return proto.Marshal(m) }
func (m *Entry) Unmarshal(data []byte) error { return proto.Unmarshal(data, m) }
