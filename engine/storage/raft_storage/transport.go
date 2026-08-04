package raft_storage

import (
	"context"
	"log"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/Aetherance/kv/engine/config"
	"github.com/Aetherance/kv/proto/pkg/metapb"
	rspb "github.com/Aetherance/kv/proto/pkg/raft_serverpb"
	"github.com/Aetherance/kv/proto/pkg/raftpb"
)

// raftConn is a pooled gRPC client stream to one store.
type raftConn struct {
	streamMu sync.Mutex
	stream   grpc.ClientStreamingClient[rspb.RaftMessage, rspb.Done]
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

func (c *raftConn) Send(msg *rspb.RaftMessage) error {
	c.streamMu.Lock()
	defer c.streamMu.Unlock()
	return c.stream.Send(msg)
}

func (c *raftConn) Stop() {
	c.cancel()
	_ = c.client.Close()
}

// ServerTransport is the fixed-topology network adapter outside raftruntime.
// Connections are resolved by the raw Raft destination ID and pooled.
type ServerTransport struct {
	cfg *config.Config
	mu  sync.RWMutex
	// store id -> connection
	conns map[uint64]*raftConn
}

func NewServerTransport(cfg *config.Config) *ServerTransport {
	return &ServerTransport{
		cfg:   cfg,
		conns: make(map[uint64]*raftConn),
	}
}

func (t *ServerTransport) Send(message *raftpb.Message) error {
	if message == nil {
		return nil
	}
	storeID := message.To
	addr, ok := t.cfg.Peers[storeID]
	if !ok {
		log.Printf("no address for store %d, drop message", storeID)
		return nil
	}
	conn, err := t.getConn(storeID, addr)
	if err != nil {
		return err
	}
	envelope := &rspb.RaftMessage{
		FromPeer: &metapb.Peer{Id: message.From, StoreId: message.From},
		ToPeer:   &metapb.Peer{Id: message.To, StoreId: message.To},
		Message:  message,
	}
	if err := conn.Send(envelope); err != nil {
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

func (t *ServerTransport) getConn(storeID uint64, addr string) (*raftConn, error) {
	t.mu.RLock()
	conn, ok := t.conns[storeID]
	t.mu.RUnlock()
	if ok {
		return conn, nil
	}
	newConn, err := newRaftConn(addr)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
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
