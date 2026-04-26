package main

import (
	"log"
	"net"

	"github.com/Aetherance/kv/engine/config"
	"github.com/Aetherance/kv/engine/storage"
	"github.com/Aetherance/kv/engine/storage/standalone"
	"github.com/Aetherance/kv/proto/pkg/kvpb"
	"github.com/Aetherance/kv/server"
	"google.golang.org/grpc"
)

func main() {
	cfg := &config.Config{DBPath: "/tmp/kv-data"}

	var db storage.Storage
	db = standalone.NewStandAloneStorage(cfg)

	if err := db.Start(); err != nil {
		log.Fatalf("start storage: %v", err)
	}
	defer db.Stop()

	svr := server.NewServer(db)

	grpcServer := grpc.NewServer()
	kvpb.RegisterKvServer(grpcServer, svr)

	lis, err := net.Listen("tcp", ":65535")
	if err != nil {
		log.Fatalf("listen: %v", err)
	}

	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("serve: %v", err)
	}
}
