package main

import (
	"flag"
	"log"
	"net"
	"strconv"
	"strings"

	"google.golang.org/grpc"

	"github.com/Aetherance/kv/engine/config"
	"github.com/Aetherance/kv/engine/storage/raft_storage"
	"github.com/Aetherance/kv/proto/pkg/clusterpb"
	"github.com/Aetherance/kv/proto/pkg/kvpb"
	rspb "github.com/Aetherance/kv/proto/pkg/raft_serverpb"
	"github.com/Aetherance/kv/server"
)

func main() {
	storeID := flag.Uint64("store-id", 1, "this store's id")
	dataDir := flag.String("data-dir", "/tmp/kv-data", "data directory")
	peersArg := flag.String("peers", "1=127.0.0.1:20161,2=127.0.0.1:20162,3=127.0.0.1:20163",
		"static cluster peers, format: id=host:port,id=host:port,...")
	flag.Parse()

	peers, err := parsePeers(*peersArg)
	if err != nil {
		log.Fatalf("parse peers: %v", err)
	}
	addr, ok := peers[*storeID]
	if !ok {
		log.Fatalf("store id %d not found in peers %q", *storeID, *peersArg)
	}

	cfg := config.NewDefaultConfig()
	cfg.StoreID = *storeID
	cfg.Peers = peers
	cfg.DBPath = *dataDir

	rs := raft_storage.NewRaftStorage(cfg)
	if err := rs.Start(); err != nil {
		log.Fatalf("start raft storage: %v", err)
	}
	defer rs.Stop()

	grpcServer := grpc.NewServer()
	kvpb.RegisterKvServer(grpcServer, server.NewServer(rs))
	rspb.RegisterRaftServiceServer(grpcServer, rs)
	clusterpb.RegisterClusterServer(grpcServer, rs)

	lis, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("listen %s: %v", addr, err)
	}
	log.Printf("store %d listening on %s", *storeID, addr)
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("serve: %v", err)
	}
}

// parsePeers parses "id=host:port,id=host:port,..." into a store id -> address map.
func parsePeers(s string) (map[uint64]string, error) {
	peers := make(map[uint64]string)
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		idStr, addr, ok := strings.Cut(part, "=")
		if !ok {
			return nil, &parseError{part}
		}
		id, err := strconv.ParseUint(strings.TrimSpace(idStr), 10, 64)
		if err != nil {
			return nil, err
		}
		peers[id] = strings.TrimSpace(addr)
	}
	return peers, nil
}

type parseError struct{ part string }

func (e *parseError) Error() string { return "invalid peer entry: " + e.part }
