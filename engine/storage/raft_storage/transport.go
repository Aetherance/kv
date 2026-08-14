package raft_storage

import (
	"context"
	"crypto/tls"
	"fmt"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/Aetherance/kv/engine/config"
	rspb "github.com/Aetherance/kv/proto/pkg/raft_serverpb"
	"github.com/Aetherance/kv/proto/pkg/raftpb"
)

// raftConn is a pooled gRPC client stream to one store.
type raftConn struct {
	streamMu sync.Mutex
	stream   grpc.ClientStreamingClient[raftpb.Message, rspb.Done]
	cancel   context.CancelFunc
	client   *grpc.ClientConn
}

func newRaftConn(addr string, tlsConfig *tls.Config) (*raftConn, error) {
	var transportCredentials credentials.TransportCredentials = insecure.NewCredentials()
	if tlsConfig != nil {
		transportCredentials = credentials.NewTLS(tlsConfig.Clone())
	}
	cc, err := grpc.NewClient(addr, grpc.WithTransportCredentials(transportCredentials))
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

func (c *raftConn) Send(msg *raftpb.Message) error {
	c.streamMu.Lock()
	defer c.streamMu.Unlock()
	return c.stream.Send(msg)
}

func (c *raftConn) Stop() {
	c.cancel()
	_ = c.client.Close()
}

// ServerTransport sends messages for the fixed-topology Raft storage.
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
		return fmt.Errorf("no address for store %d", storeID)
	}
	conn, err := t.getConn(storeID, addr)
	if err != nil {
		return err
	}
	if err := conn.Send(message); err != nil {
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
	newConn, err := newRaftConn(addr, t.cfg.RaftTLSConfig)
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
