package raft_storage

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	"github.com/Aetherance/kv/engine/config"
	"github.com/Aetherance/kv/proto/pkg/clusterpb"
	rspb "github.com/Aetherance/kv/proto/pkg/raft_serverpb"
	"github.com/Aetherance/kv/proto/pkg/raftpb"
)

func TestServerTransportRoutesEnvelopeAndRefreshesAddress(t *testing.T) {
	firstAddress, firstReceived := startRecordingRaftServer(t)
	secondAddress, secondReceived := startRecordingRaftServer(t)
	members := map[uint64]string{2: firstAddress}
	transport := NewServerTransport(42, members)
	t.Cleanup(transport.Stop)

	// The transport owns its routing snapshot; caller mutation must not change it.
	members[2] = secondAddress
	first := &raftpb.Message{MsgType: raftpb.MessageType_MsgAppend, From: 1, To: 2, Term: 3}
	if err := transport.Send(first); err != nil {
		t.Fatalf("send to first address: %v", err)
	}
	assertEnvelope(t, receiveEnvelope(t, firstReceived), 42, first)

	transport.ReplaceMembers(map[uint64]string{2: secondAddress})
	second := &raftpb.Message{MsgType: raftpb.MessageType_MsgHeartbeat, From: 1, To: 2, Term: 4}
	if err := transport.Send(second); err != nil {
		t.Fatalf("send to replacement address: %v", err)
	}
	assertEnvelope(t, receiveEnvelope(t, secondReceived), 42, second)

	transport.ReplaceMembers(nil)
	if err := transport.Send(second); err == nil || !strings.Contains(err.Error(), "no address for store 2") {
		t.Fatalf("send to removed member error = %v", err)
	}
}

func TestAppliedMembershipRefreshesTransport(t *testing.T) {
	store := startMembershipStore(t)
	member := &clusterpb.Member{Id: 2, RaftAddress: "127.0.0.1:2"}
	if err := store.proposeMembership(context.Background(), raftpb.ConfChangeType_AddLearnerNode, member); err != nil {
		t.Fatalf("add learner: %v", err)
	}

	store.transport.mu.RLock()
	address, exists := store.transport.members[2]
	store.transport.mu.RUnlock()
	if !exists || address != member.RaftAddress {
		t.Fatalf("transport route for member 2 = %q, %v", address, exists)
	}
}

func TestRaftRejectsEnvelopeFromAnotherCluster(t *testing.T) {
	store := &RaftStorage{
		config:    &config.Config{StoreID: 1},
		clusterID: 42,
	}
	stream := &fakeRaftServerStream{
		ctx: context.Background(),
		envelopes: []*rspb.RaftEnvelope{{
			ClusterId: 99,
			Message:   &raftpb.Message{From: 2, To: 1},
		}},
	}
	err := store.Raft(stream)
	if err == nil || !strings.Contains(err.Error(), "cluster ID mismatch: got 99, expected 42") {
		t.Fatalf("cluster mismatch error = %v", err)
	}
}

type recordingRaftService struct {
	rspb.UnimplementedRaftServiceServer
	received chan *rspb.RaftEnvelope
}

func (s *recordingRaftService) Raft(stream rspb.RaftService_RaftServer) error {
	for {
		envelope, err := stream.Recv()
		if err == io.EOF {
			return stream.SendAndClose(&rspb.Done{})
		}
		if err != nil {
			return err
		}
		cloned := proto.Clone(envelope).(*rspb.RaftEnvelope)
		select {
		case s.received <- cloned:
		case <-stream.Context().Done():
			return stream.Context().Err()
		}
	}
}

func startRecordingRaftServer(t *testing.T) (string, <-chan *rspb.RaftEnvelope) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	received := make(chan *rspb.RaftEnvelope, 4)
	server := grpc.NewServer()
	rspb.RegisterRaftServiceServer(server, &recordingRaftService{received: received})
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
		select {
		case err := <-serveErr:
			if err != nil && !errors.Is(err, grpc.ErrServerStopped) {
				t.Errorf("serve: %v", err)
			}
		case <-time.After(time.Second):
			t.Error("gRPC server did not stop")
		}
	})
	return listener.Addr().String(), received
}

func receiveEnvelope(t *testing.T, received <-chan *rspb.RaftEnvelope) *rspb.RaftEnvelope {
	t.Helper()
	select {
	case envelope := <-received:
		return envelope
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for raft envelope")
		return nil
	}
}

func assertEnvelope(t *testing.T, envelope *rspb.RaftEnvelope, clusterID uint64, message *raftpb.Message) {
	t.Helper()
	if envelope.ClusterId != clusterID || !proto.Equal(envelope.Message, message) {
		t.Fatalf("envelope = %v, want cluster %d message %v", envelope, clusterID, message)
	}
}

type fakeRaftServerStream struct {
	grpc.ServerStream
	ctx       context.Context
	envelopes []*rspb.RaftEnvelope
	response  *rspb.Done
}

func (s *fakeRaftServerStream) Context() context.Context { return s.ctx }

func (s *fakeRaftServerStream) Recv() (*rspb.RaftEnvelope, error) {
	if len(s.envelopes) == 0 {
		return nil, io.EOF
	}
	envelope := s.envelopes[0]
	s.envelopes = s.envelopes[1:]
	return envelope, nil
}

func (s *fakeRaftServerStream) SendAndClose(response *rspb.Done) error {
	s.response = response
	return nil
}
