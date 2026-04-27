package integration_test

import (
	"context"
	"net"
	"testing"

	"github.com/Aetherance/kv/engine/config"
	"github.com/Aetherance/kv/engine/storage/standalone"
	"github.com/Aetherance/kv/proto/pkg/kvpb"
	"github.com/Aetherance/kv/proto/pkg/kvrpcpb"
	"github.com/Aetherance/kv/server"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

func setupTest(t *testing.T) kvpb.KvClient {
	t.Helper()

	store := standalone.NewStandAloneStorage(&config.Config{DBPath: t.TempDir()})
	if err := store.Start(); err != nil {
		t.Fatalf("start storage: %v", err)
	}

	svr := server.NewServer(store)

	lis := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer()
	kvpb.RegisterKvServer(grpcServer, svr)
	go grpcServer.Serve(lis)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}

	t.Cleanup(func() { conn.Close(); grpcServer.Stop(); store.Stop() })

	return kvpb.NewKvClient(conn)
}

func TestCRUD(t *testing.T) {
	client := setupTest(t)

	t.Run("PutGet", func(t *testing.T) {
		_, err := client.KvPut(context.Background(), &kvrpcpb.KvPutRequest{
			Cf: "default", Key: []byte("a"), Value: []byte("hello"),
		})
		if err != nil {
			t.Fatalf("KvPut: %v", err)
		}

		resp, err := client.KvGet(context.Background(), &kvrpcpb.KvGetRequest{
			Cf: "default", Key: []byte("a"),
		})
		if err != nil {
			t.Fatalf("KvGet: %v", err)
		}
		if resp.NotFound {
			t.Fatal("expected value, got NotFound")
		}
		if string(resp.Value) != "hello" {
			t.Fatalf("expected 'hello', got %q", string(resp.Value))
		}
	})

	t.Run("GetNotFound", func(t *testing.T) {
		resp, err := client.KvGet(context.Background(), &kvrpcpb.KvGetRequest{
			Cf: "default", Key: []byte("nonexistent"),
		})
		if err != nil {
			t.Fatalf("KvGet: %v", err)
		}
		if !resp.NotFound {
			t.Fatal("expected NotFound for missing key")
		}
		if resp.Value != nil {
			t.Fatalf("expected nil value, got %q", string(resp.Value))
		}
	})

	t.Run("PutOverwrite", func(t *testing.T) {
		_, err := client.KvPut(context.Background(), &kvrpcpb.KvPutRequest{
			Cf: "default", Key: []byte("a"), Value: []byte("world"),
		})
		if err != nil {
			t.Fatalf("KvPut: %v", err)
		}

		resp, err := client.KvGet(context.Background(), &kvrpcpb.KvGetRequest{
			Cf: "default", Key: []byte("a"),
		})
		if err != nil {
			t.Fatalf("KvGet: %v", err)
		}
		if string(resp.Value) != "world" {
			t.Fatalf("expected 'world', got %q", string(resp.Value))
		}
	})

	t.Run("Delete", func(t *testing.T) {
		_, err := client.KvDelete(context.Background(), &kvrpcpb.KvDeleteRequest{
			Cf: "default", Key: []byte("a"),
		})
		if err != nil {
			t.Fatalf("KvDelete: %v", err)
		}

		resp, err := client.KvGet(context.Background(), &kvrpcpb.KvGetRequest{
			Cf: "default", Key: []byte("a"),
		})
		if err != nil {
			t.Fatalf("KvGet: %v", err)
		}
		if !resp.NotFound {
			t.Fatal("expected NotFound after delete")
		}
	})

	t.Run("DeleteNonExistent", func(t *testing.T) {
		_, err := client.KvDelete(context.Background(), &kvrpcpb.KvDeleteRequest{
			Cf: "default", Key: []byte("does-not-exist"),
		})
		if err != nil {
			t.Fatalf("delete non-existent: %v", err)
		}
	})
}

func TestScan(t *testing.T) {
	client := setupTest(t)

	for _, kv := range []struct{ k, v string }{
		{"a", "1"}, {"b", "2"}, {"c", "3"},
	} {
		_, err := client.KvPut(context.Background(), &kvrpcpb.KvPutRequest{
			Cf: "default", Key: []byte(kv.k), Value: []byte(kv.v),
		})
		if err != nil {
			t.Fatalf("setup KvPut: %v", err)
		}
	}

	tests := []struct {
		name  string
		start string
		limit uint32
		want  int
		first string
	}{
		{name: "all", start: "", limit: 10, want: 3, first: "a"},
		{name: "with limit", start: "", limit: 1, want: 1, first: "a"},
		{name: "with offset", start: "b", limit: 10, want: 2, first: "b"},
		{name: "past end", start: "z", limit: 10, want: 0, first: ""},
		{name: "limit zero", start: "", limit: 0, want: 0, first: ""},
		{name: "single key", start: "b", limit: 1, want: 1, first: "b"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := client.KvScan(context.Background(), &kvrpcpb.KvScanRequest{
				Cf: "default", StartKey: []byte(tc.start), Limit: tc.limit,
			})
			if err != nil {
				t.Fatalf("KvScan: %v", err)
			}
			if len(resp.Kvs) != tc.want {
				t.Fatalf("expected %d results, got %d", tc.want, len(resp.Kvs))
			}
			if tc.want > 0 && string(resp.Kvs[0].Key) != tc.first {
				t.Fatalf("expected first key %q, got %q", tc.first, string(resp.Kvs[0].Key))
			}
		})
	}
}

func TestCFReadIsolation(t *testing.T) {
	client := setupTest(t)

	_, err := client.KvPut(context.Background(), &kvrpcpb.KvPutRequest{
		Cf: "cf1", Key: []byte("k"), Value: []byte("from-cf1"),
	})
	if err != nil {
		t.Fatalf("KvPut cf1: %v", err)
	}
	_, err = client.KvPut(context.Background(), &kvrpcpb.KvPutRequest{
		Cf: "cf2", Key: []byte("k"), Value: []byte("from-cf2"),
	})
	if err != nil {
		t.Fatalf("KvPut cf2: %v", err)
	}

	resp, err := client.KvGet(context.Background(), &kvrpcpb.KvGetRequest{
		Cf: "cf1", Key: []byte("k"),
	})
	if err != nil {
		t.Fatalf("KvGet cf1: %v", err)
	}
	if string(resp.Value) != "from-cf1" {
		t.Fatalf("expected 'from-cf1', got %q", string(resp.Value))
	}

	resp, err = client.KvGet(context.Background(), &kvrpcpb.KvGetRequest{
		Cf: "cf2", Key: []byte("k"),
	})
	if err != nil {
		t.Fatalf("KvGet cf2: %v", err)
	}
	if string(resp.Value) != "from-cf2" {
		t.Fatalf("expected 'from-cf2', got %q", string(resp.Value))
	}
}

func TestCFScanBoundary(t *testing.T) {
	client := setupTest(t)

	_, err := client.KvPut(context.Background(), &kvrpcpb.KvPutRequest{
		Cf: "cf1", Key: []byte("a"), Value: []byte("va"),
	})
	if err != nil {
		t.Fatalf("KvPut cf1/a: %v", err)
	}
	_, err = client.KvPut(context.Background(), &kvrpcpb.KvPutRequest{
		Cf: "cf2", Key: []byte("a"), Value: []byte("vb"),
	})
	if err != nil {
		t.Fatalf("KvPut cf2/a: %v", err)
	}

	resp, err := client.KvScan(context.Background(), &kvrpcpb.KvScanRequest{
		Cf: "cf1", StartKey: []byte(""), Limit: 10,
	})
	if err != nil {
		t.Fatalf("KvScan cf1: %v", err)
	}
	if len(resp.Kvs) != 1 {
		t.Fatalf("expected 1 result in cf1, got %d", len(resp.Kvs))
	}
	if string(resp.Kvs[0].Value) != "va" {
		t.Fatalf("expected 'va', got %q", string(resp.Kvs[0].Value))
	}
}

func TestEmptyCF(t *testing.T) {
	client := setupTest(t)

	_, err := client.KvPut(context.Background(), &kvrpcpb.KvPutRequest{
		Cf: "", Key: []byte("k"), Value: []byte("v"),
	})
	if err != nil {
		t.Fatalf("KvPut empty CF: %v", err)
	}

	resp, err := client.KvGet(context.Background(), &kvrpcpb.KvGetRequest{
		Cf: "", Key: []byte("k"),
	})
	if err != nil {
		t.Fatalf("KvGet empty CF: %v", err)
	}
	if resp.NotFound {
		t.Fatal("unexpected NotFound for empty CF")
	}
	if string(resp.Value) != "v" {
		t.Fatalf("expected 'v', got %q", string(resp.Value))
	}
}

func TestEdgeCases(t *testing.T) {
	client := setupTest(t)

	t.Run("empty key", func(t *testing.T) {
		_, err := client.KvPut(context.Background(), &kvrpcpb.KvPutRequest{
			Cf: "default", Key: []byte{}, Value: []byte("v"),
		})
		if err != nil {
			t.Fatalf("KvPut empty key: %v", err)
		}

		resp, err := client.KvGet(context.Background(), &kvrpcpb.KvGetRequest{
			Cf: "default", Key: []byte{},
		})
		if err != nil {
			t.Fatalf("KvGet empty key: %v", err)
		}
		if string(resp.Value) != "v" {
			t.Fatalf("expected 'v', got %q", string(resp.Value))
		}

		_, err = client.KvDelete(context.Background(), &kvrpcpb.KvDeleteRequest{
			Cf: "default", Key: []byte{},
		})
		if err != nil {
			t.Fatalf("KvDelete empty key: %v", err)
		}
	})

	t.Run("binary key and value", func(t *testing.T) {
		binKey := []byte{0x00, 0x01, 0x02, 0xFF}
		binVal := []byte{0xDE, 0xAD, 0xBE, 0xEF}

		_, err := client.KvPut(context.Background(), &kvrpcpb.KvPutRequest{
			Cf: "default", Key: binKey, Value: binVal,
		})
		if err != nil {
			t.Fatalf("KvPut binary: %v", err)
		}

		resp, err := client.KvGet(context.Background(), &kvrpcpb.KvGetRequest{
			Cf: "default", Key: binKey,
		})
		if err != nil {
			t.Fatalf("KvGet binary: %v", err)
		}
		if len(resp.Value) != 4 || resp.Value[0] != 0xDE || resp.Value[3] != 0xEF {
			t.Fatalf("unexpected binary value: %v", resp.Value)
		}
	})

	t.Run("large value", func(t *testing.T) {
		large := make([]byte, 64*1024)
		for i := range large {
			large[i] = byte(i % 256)
		}

		_, err := client.KvPut(context.Background(), &kvrpcpb.KvPutRequest{
			Cf: "default", Key: []byte("large"), Value: large,
		})
		if err != nil {
			t.Fatalf("KvPut large: %v", err)
		}

		resp, err := client.KvGet(context.Background(), &kvrpcpb.KvGetRequest{
			Cf: "default", Key: []byte("large"),
		})
		if err != nil {
			t.Fatalf("KvGet large: %v", err)
		}
		if len(resp.Value) != 64*1024 {
			t.Fatalf("expected 64KB, got %d bytes", len(resp.Value))
		}
		for i, b := range resp.Value {
			if b != byte(i%256) {
				t.Fatalf("corrupt at byte %d: expected %d, got %d", i, byte(i%256), b)
			}
		}
	})
}
