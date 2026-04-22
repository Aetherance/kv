package standalone

import (
	"testing"

	"github.com/Aetherance/kv/engine/config"
	"github.com/Aetherance/kv/engine/storage"
)

func newTestStandAloneStorage(t *testing.T) *StandAloneStorage {
	t.Helper()

	s := NewStandAloneStorage(&config.Config{
		DBPath: t.TempDir(),
	})
	if err := s.Start(); err != nil {
		t.Fatalf("start storage: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Stop(); err != nil {
			t.Fatalf("stop storage: %v", err)
		}
	})
	return s
}

func TestStandAloneStorageRequiresStart(t *testing.T) {
	s := NewStandAloneStorage(&config.Config{
		DBPath: t.TempDir(),
	})

	reader, err := s.Reader(nil)
	if err == nil {
		t.Fatalf("expected reader error before start")
	}
	if reader != nil {
		t.Fatalf("expected nil reader before start")
	}

	err = s.Write(nil, []storage.Modify{
		{
			Data: storage.Put{
				Cf:  "default",
				Key: []byte("k1"),
				Val: []byte("v1"),
			},
		},
	})
	if err == nil {
		t.Fatalf("expected write error before start")
	}

	if err := s.Stop(); err != nil {
		t.Fatalf("stop before start: %v", err)
	}
}

func TestStandAloneStorageReadWriteAndDelete(t *testing.T) {
	s := newTestStandAloneStorage(t)

	err := s.Write(nil, []storage.Modify{
		{
			Data: storage.Put{
				Cf:  "default",
				Key: []byte("a"),
				Val: []byte("va"),
			},
		},
		{
			Data: storage.Put{
				Cf:  "default",
				Key: []byte("b"),
				Val: []byte("vb"),
			},
		},
		{
			Data: storage.Put{
				Cf:  "lock",
				Key: []byte("a"),
				Val: []byte("la"),
			},
		},
	})
	if err != nil {
		t.Fatalf("write puts: %v", err)
	}

	reader, err := s.Reader(nil)
	if err != nil {
		t.Fatalf("create reader: %v", err)
	}

	value, err := reader.GetCF("default", []byte("a"))
	if err != nil {
		t.Fatalf("get default/a: %v", err)
	}
	if string(value) != "va" {
		t.Fatalf("unexpected value for default/a: %q", string(value))
	}

	value, err = reader.GetCF("lock", []byte("a"))
	if err != nil {
		t.Fatalf("get lock/a: %v", err)
	}
	if string(value) != "la" {
		t.Fatalf("unexpected value for lock/a: %q", string(value))
	}

	value, err = reader.GetCF("default", []byte("missing"))
	if err != nil {
		t.Fatalf("get missing: %v", err)
	}
	if value != nil {
		t.Fatalf("expected nil for missing key, got %q", string(value))
	}

	iter := reader.IterCF("default")

	iter.Seek([]byte("a"))
	if !iter.Valid() {
		t.Fatalf("expected iterator to be valid at first default key")
	}

	item := iter.Item()
	if got := string(item.Key()); got != "a" {
		t.Fatalf("unexpected first iterator key: %q", got)
	}
	value, err = item.Value()
	if err != nil {
		t.Fatalf("read first iterator value: %v", err)
	}
	if string(value) != "va" {
		t.Fatalf("unexpected first iterator value: %q", string(value))
	}

	iter.Next()
	if !iter.Valid() {
		t.Fatalf("expected iterator to be valid at second default key")
	}

	item = iter.Item()
	if got := string(item.KeyCopy(nil)); got != "b" {
		t.Fatalf("unexpected second iterator key: %q", got)
	}
	if got := item.ValueSize(); got != 2 {
		t.Fatalf("unexpected second iterator value size: %d", got)
	}
	value, err = item.ValueCopy(nil)
	if err != nil {
		t.Fatalf("read second iterator value: %v", err)
	}
	if string(value) != "vb" {
		t.Fatalf("unexpected second iterator value: %q", string(value))
	}

	iter.Next()
	if iter.Valid() {
		t.Fatalf("expected iterator to stop at default prefix boundary")
	}
	iter.Close()
	reader.Close()

	if err := s.Write(nil, []storage.Modify{
		{
			Data: storage.Delete{
				Cf:  "default",
				Key: []byte("a"),
			},
		},
	}); err != nil {
		t.Fatalf("delete key: %v", err)
	}

	reader.Close()

	reader, err = s.Reader(nil)
	if err != nil {
		t.Fatalf("create second reader: %v", err)
	}
	defer reader.Close()

	value, err = reader.GetCF("default", []byte("a"))
	if err != nil {
		t.Fatalf("get deleted key: %v", err)
	}
	if value != nil {
		t.Fatalf("expected deleted key to be absent, got %q", string(value))
	}
}

func TestStandAloneStorageWriteRejectsInvalidBatch(t *testing.T) {
	s := newTestStandAloneStorage(t)

	err := s.Write(nil, []storage.Modify{
		{Data: "bad"},
	})
	if err == nil {
		t.Fatalf("expected invalid batch error")
	}
}

func TestStandAloneStorageStopClearsDB(t *testing.T) {
	s := newTestStandAloneStorage(t)

	if err := s.Stop(); err != nil {
		t.Fatalf("stop storage: %v", err)
	}
	if s.db != nil {
		t.Fatalf("expected db to be nil after stop")
	}
	if err := s.Stop(); err != nil {
		t.Fatalf("stop storage twice: %v", err)
	}
}
