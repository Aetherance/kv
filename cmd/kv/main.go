package main

import (
	"context"
	"flag"
	"log"
	"net"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

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
		"initial cluster members, format: id=host:port,id=host:port,...")
	clusterID := flag.Uint64("cluster-id", 1, "cluster identity for initial bootstrap")
	raftAddress := flag.String("raft-address", "", "this member's advertised Raft address (defaults to its --peers entry)")
	listenAddress := flag.String("listen-address", "", "gRPC listen address (defaults to --raft-address)")
	joinAddress := flag.String("join", "", "existing member address used to join a cluster with a fresh data directory after member add --learner")
	flag.Parse()

	peers, err := parsePeers(*peersArg)
	if err != nil {
		log.Fatalf("parse peers: %v", err)
	}
	addr := strings.TrimSpace(*raftAddress)
	if addr == "" {
		addr = peers[*storeID]
	}
	if addr == "" {
		log.Fatalf("store id %d has no address in peers %q and --raft-address is empty", *storeID, *peersArg)
	}
	peers[*storeID] = addr
	listenAddr := strings.TrimSpace(*listenAddress)
	if listenAddr == "" {
		listenAddr = addr
	}
	joinEndpoint := strings.TrimSpace(*joinAddress)
	join := joinEndpoint != ""
	if join {
		joinInfo, err := fetchJoinInfo(joinEndpoint, *storeID, addr)
		if err != nil {
			log.Fatalf("join cluster: %v", err)
		}
		*clusterID = joinInfo.ClusterId
		peers = make(map[uint64]string, len(joinInfo.Members))
		for _, member := range joinInfo.Members {
			peers[member.Id] = member.RaftAddress
		}
	}

	cfg := config.NewDefaultConfig()
	cfg.StoreID = *storeID
	cfg.ClusterID = *clusterID
	cfg.Peers = peers
	cfg.Join = join
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

	lis, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Fatalf("listen %s: %v", listenAddr, err)
	}
	log.Printf("store %d listening on %s (advertised raft address %s, cluster %d)", *storeID, listenAddr, addr, *clusterID)
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("serve: %v", err)
	}
}

func fetchJoinInfo(endpoint string, id uint64, address string) (*clusterpb.JoinInfoResponse, error) {
	conn, err := grpc.NewClient(endpoint, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return clusterpb.NewClusterClient(conn).JoinInfo(ctx, &clusterpb.JoinInfoRequest{Id: id, RaftAddress: address})
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
