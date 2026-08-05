package server

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Aetherance/kv/engine/storage"
	engine_util "github.com/Aetherance/kv/engine/util"
	"github.com/Aetherance/kv/proto/pkg/kvrpcpb"
)

type testStorage struct {
	checkContext func(context.Context) error
	onReader     func()
	reader       storage.StorageReader
}

func (s *testStorage) Start() error { return nil }
func (s *testStorage) Stop() error  { return nil }

func (s *testStorage) Write(ctx context.Context, _ []storage.Modify) error {
	if s.checkContext == nil {
		return nil
	}
	return s.checkContext(ctx)
}

func (s *testStorage) Reader(ctx context.Context) (storage.StorageReader, error) {
	if s.checkContext != nil {
		if err := s.checkContext(ctx); err != nil {
			return nil, err
		}
	}
	if s.onReader != nil {
		s.onReader()
	}
	return s.reader, nil
}

type testReader struct {
	value    []byte
	iterator engine_util.Iterator
}

func (r *testReader) GetCF(string, []byte) ([]byte, error) { return r.value, nil }
func (r *testReader) IterCF(string) engine_util.Iterator   { return r.iterator }
func (r *testReader) Close()                               {}

type singleIterator struct {
	valid bool
}

func (i *singleIterator) Item() engine_util.Item { return testItem{} }
func (i *singleIterator) Valid() bool            { return i.valid }
func (i *singleIterator) Next()                  { i.valid = false }
func (i *singleIterator) Seek([]byte)            { i.valid = true }
func (i *singleIterator) Close()                 {}

type testItem struct{}

func (testItem) Key() []byte                      { return []byte("key") }
func (testItem) KeyCopy([]byte) []byte            { return []byte("key") }
func (testItem) Value() ([]byte, error)           { return []byte("value"), nil }
func (testItem) ValueSize() int                   { return len("value") }
func (testItem) ValueCopy([]byte) ([]byte, error) { return []byte("value"), nil }

func TestServerPropagatesRequestContext(t *testing.T) {
	type contextKey struct{}
	const marker = "request-context"

	checkContext := func(ctx context.Context) error {
		if got := ctx.Value(contextKey{}); got != marker {
			return fmt.Errorf("context marker = %v, want %q", got, marker)
		}
		return nil
	}
	store := &testStorage{
		checkContext: checkContext,
		reader:       &testReader{value: []byte("value")},
	}
	server := NewServer(store)
	ctx := context.WithValue(context.Background(), contextKey{}, marker)

	if _, err := server.KvPut(ctx, &kvrpcpb.KvPutRequest{}); err != nil {
		t.Fatalf("put: %v", err)
	}
	response, err := server.KvGet(ctx, &kvrpcpb.KvGetRequest{})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got := string(response.Value); got != "value" {
		t.Fatalf("value = %q, want %q", got, "value")
	}
}

func TestKvScanHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	store := &testStorage{
		onReader: cancel,
		reader: &testReader{
			iterator: &singleIterator{},
		},
	}

	_, err := NewServer(store).KvScan(ctx, &kvrpcpb.KvScanRequest{Limit: 1})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("scan error = %v, want context.Canceled", err)
	}
}
