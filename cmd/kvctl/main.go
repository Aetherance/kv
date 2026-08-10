package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/Aetherance/kv/proto/pkg/clusterpb"
	"github.com/Aetherance/kv/proto/pkg/kvpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	addr := flag.String("a", ":65535", "server address")
	flag.Parse()

	if flag.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "usage: kvctl -a <addr> <command> [args...]\n")
		os.Exit(1)
	}

	conn, err := grpc.NewClient(*addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	client := kvpb.NewKvClient(conn)
	clusterClient := clusterpb.NewClusterClient(conn)

	head := flag.Args()
	cmd := head[0]
	args := head[1:]

	switch cmd {
	case "get":
		runGet(client, args)
	case "put":
		runPut(client, args)
	case "del":
		runDel(client, args)
	case "scan":
		runScan(client, args)
	case "member":
		runMember(clusterClient, args)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		os.Exit(1)
	}
}
