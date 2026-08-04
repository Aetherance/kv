package raft_storage

import (
	"testing"

	"github.com/Aetherance/kv/engine/config"
	"github.com/Aetherance/kv/engine/storage"
)

func TestSingleNodeWriteReadAndRestart(t *testing.T) {
	directory := t.TempDir()
	newConfig := func() *config.Config {
		cfg := config.NewDefaultConfig()
		cfg.StoreID = 1
		cfg.Peers = map[uint64]string{1: "127.0.0.1:1"}
		cfg.DBPath = directory
		cfg.RaftLogGcCountLimit = 2
		return cfg
	}

	first := NewRaftStorage(newConfig())
	if err := first.Start(); err != nil {
		t.Fatalf("start first instance: %v", err)
	}
	if err := first.Write([]storage.Modify{{Data: storage.Put{
		Cf: "default", Key: []byte("key"), Val: []byte("value"),
	}}}); err != nil {
		t.Fatalf("write: %v", err)
	}
	assertStoredValue(t, first, "value")
	if err := first.Stop(); err != nil {
		t.Fatalf("stop first instance: %v", err)
	}

	second := NewRaftStorage(newConfig())
	if err := second.Start(); err != nil {
		t.Fatalf("restart: %v", err)
	}
	t.Cleanup(func() {
		if err := second.Stop(); err != nil {
			t.Errorf("stop restarted instance: %v", err)
		}
	})
	assertStoredValue(t, second, "value")
}

func assertStoredValue(t *testing.T, store *RaftStorage, expected string) {
	t.Helper()
	reader, err := store.Reader()
	if err != nil {
		t.Fatalf("reader: %v", err)
	}
	defer reader.Close()
	value, err := reader.GetCF("default", []byte("key"))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(value) != expected {
		t.Fatalf("value %q, expected %q", value, expected)
	}
}
