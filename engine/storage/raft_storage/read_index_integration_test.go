package raft_storage

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"

	"github.com/Aetherance/kv/engine/config"
	"github.com/Aetherance/kv/engine/storage"
	rspb "github.com/Aetherance/kv/proto/pkg/raft_serverpb"
)

func TestFollowerReadIndexReturnsLatestCommittedValue(t *testing.T) {
	const clusterSize = 3
	listeners := make([]net.Listener, clusterSize)
	peers := make(map[uint64]string, clusterSize)
	for i := range listeners {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen for store %d: %v", i+1, err)
		}
		listeners[i] = listener
		peers[uint64(i+1)] = listener.Addr().String()
	}

	stores := make([]*RaftStorage, clusterSize)
	servers := make([]*grpc.Server, clusterSize)
	for i := range stores {
		cfg := config.NewDefaultConfig()
		cfg.StoreID = uint64(i + 1)
		cfg.Peers = peers
		cfg.DBPath = t.TempDir()
		cfg.RaftBaseTickInterval = 10 * time.Millisecond

		stores[i] = NewRaftStorage(cfg)
		servers[i] = grpc.NewServer()
		rspb.RegisterRaftServiceServer(servers[i], stores[i])
		go func(server *grpc.Server, listener net.Listener) {
			_ = server.Serve(listener)
		}(servers[i], listeners[i])
	}
	t.Cleanup(func() {
		for _, store := range stores {
			if err := store.Stop(); err != nil {
				t.Errorf("stop store: %v", err)
			}
		}
		for _, server := range servers {
			server.Stop()
		}
	})
	for i, store := range stores {
		if err := store.Start(); err != nil {
			t.Fatalf("start store %d: %v", i+1, err)
		}
	}

	leaderIndex := waitForWritableStore(t, stores)
	for i, follower := range stores {
		if i == leaderIndex {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		reader, err := follower.Reader(ctx)
		if err != nil {
			cancel()
			t.Fatalf("follower %d reader: %v", i+1, err)
		}
		value, err := reader.GetCF("default", []byte("key"))
		reader.Close()
		cancel()
		if err != nil {
			t.Fatalf("follower %d get: %v", i+1, err)
		}
		if string(value) != "value" {
			t.Fatalf("follower %d value = %q, want value", i+1, value)
		}
	}
}

func waitForWritableStore(t *testing.T, stores []*RaftStorage) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for i, store := range stores {
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			err := store.Write(ctx, []storage.Modify{{Data: storage.Put{
				Cf: "default", Key: []byte("key"), Val: []byte("value"),
			}}})
			cancel()
			if err == nil {
				return i
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no writable leader elected within timeout")
	return -1
}
