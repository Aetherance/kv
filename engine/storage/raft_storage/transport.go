package raft_storage

import (
	"context"
	"fmt"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	rspb "github.com/Aetherance/kv/proto/pkg/raft_serverpb"
	"github.com/Aetherance/kv/proto/pkg/raftpb"
)

// raftConn is a pooled gRPC client stream to one store.
type raftConn struct {
	streamMu sync.Mutex
	stream   grpc.ClientStreamingClient[rspb.RaftEnvelope, rspb.Done]
	cancel   context.CancelFunc
	client   *grpc.ClientConn
}

func newRaftConn(addr string) (*raftConn, error) {
	cc, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := rspb.NewRaftServiceClient(cc).Raft(ctx)
	if err != nil {
		cancel()
		_ = cc.Close()
		return nil, err
	}
	return &raftConn{stream: stream, cancel: cancel, client: cc}, nil
}

func (c *raftConn) Send(msg *rspb.RaftEnvelope) error {
	c.streamMu.Lock()
	defer c.streamMu.Unlock()
	return c.stream.Send(msg)
}

func (c *raftConn) Stop() {
	c.cancel()
	_ = c.client.Close()
}

// ServerTransport resolves destinations from the applied cluster metadata.
// Replacing the member map also drops connections whose address changed or
// whose member was removed.
type ServerTransport struct {
	clusterID uint64
	mu        sync.RWMutex
	members   map[uint64]string
	// store id -> connection
	conns map[uint64]*raftConn
}

func NewServerTransport(clusterID uint64, members map[uint64]string) *ServerTransport {
	return &ServerTransport{
		clusterID: clusterID,
		members:   cloneAddresses(members),
		conns:     make(map[uint64]*raftConn),
	}
}

func (t *ServerTransport) Send(message *raftpb.Message) error {
	if message == nil {
		return nil
	}
	storeID := message.To
	t.mu.RLock()
	addr, ok := t.members[storeID]
	t.mu.RUnlock()
	if !ok {
		return fmt.Errorf("no address for store %d", storeID)
	}
	conn, err := t.getConn(storeID, addr)
	if err != nil {
		return err
	}
	if err := conn.Send(&rspb.RaftEnvelope{ClusterId: t.clusterID, Message: message}); err != nil {
		// Drop the broken connection so the next send reconnects.
		t.mu.Lock()
		if t.conns[storeID] == conn {
			conn.Stop()
			delete(t.conns, storeID)
		}
		t.mu.Unlock()
		return err
	}
	return nil
}

func (t *ServerTransport) ReplaceMembers(members map[uint64]string) {
	members = cloneAddresses(members)
	t.mu.Lock()
	defer t.mu.Unlock()

	for id, conn := range t.conns {
		oldAddress := t.members[id]
		newAddress, exists := members[id]
		if !exists || newAddress != oldAddress {
			conn.Stop()
			delete(t.conns, id)
		}
	}
	t.members = members
}

func (t *ServerTransport) getConn(storeID uint64, addr string) (*raftConn, error) {
	t.mu.RLock()
	currentAddress, memberExists := t.members[storeID]
	conn, ok := t.conns[storeID]
	t.mu.RUnlock()
	if !memberExists || currentAddress != addr {
		return nil, fmt.Errorf("address for store %d changed while connecting", storeID)
	}
	if ok {
		return conn, nil
	}
	newConn, err := newRaftConn(addr)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	currentAddress, memberExists = t.members[storeID]
	if !memberExists || currentAddress != addr {
		newConn.Stop()
		return nil, fmt.Errorf("address for store %d changed while connecting", storeID)
	}
	if conn, ok := t.conns[storeID]; ok {
		newConn.Stop()
		return conn, nil
	}
	t.conns[storeID] = newConn
	return newConn, nil
}

func (t *ServerTransport) Stop() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, conn := range t.conns {
		conn.Stop()
	}
	t.conns = make(map[uint64]*raftConn)
}

func cloneAddresses(addresses map[uint64]string) map[uint64]string {
	cloned := make(map[uint64]string, len(addresses))
	for id, address := range addresses {
		cloned[id] = address
	}
	return cloned
}
