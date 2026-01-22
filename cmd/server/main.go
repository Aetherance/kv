package main

import (
	"github.com/Aetherance/kv/coord"
	"github.com/Aetherance/kv/engine"
	redis_protocol "github.com/Aetherance/kv/protocol/redis"
	"github.com/Aetherance/kv/server"
	"github.com/Aetherance/kv/storage/memory"
)

func main() {
	storage := memory.NewMemoryStorage()
	kv := engine.New(storage)
	lc := coord.NewLocal(kv)

	serverOpts := []server.Option{
		server.WithCoordinator(lc),
		server.WithExporter(redis_protocol.NewExporter(), ":1234"),
	}

	srv, err := server.New(serverOpts...)
	if err != nil {
		panic(err)
	}

	srv.Run()
}
