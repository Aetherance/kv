package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/Aetherance/kv/proto/pkg/kvpb"
	"github.com/Aetherance/kv/proto/pkg/kvrpcpb"
)

func runGet(client kvpb.KvClient, args []string) {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: get <cf> <key>")
		os.Exit(1)
	}
	req := &kvrpcpb.KvGetRequest{Cf: args[0], Key: []byte(args[1])}
	resp, err := client.KvGet(context.Background(), req)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if resp.NotFound {
		fmt.Println("(nil)")
		return
	}
	fmt.Printf("%s\n", resp.Value)
}

func runPut(client kvpb.KvClient, args []string) {
	if len(args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: put <cf> <key> <value>")
		os.Exit(1)
	}
	req := &kvrpcpb.KvPutRequest{Cf: args[0], Key: []byte(args[1]), Value: []byte(args[2])}
	_, err := client.KvPut(context.Background(), req)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("OK")
}

func runDel(client kvpb.KvClient, args []string) {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: del <cf> <key>")
		os.Exit(1)
	}
	req := &kvrpcpb.KvDeleteRequest{Cf: args[0], Key: []byte(args[1])}
	_, err := client.KvDelete(context.Background(), req)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("OK")
}

func runScan(client kvpb.KvClient, args []string) {
	fs := flag.NewFlagSet("scan", flag.ExitOnError)
	limit := fs.Int("limit", 10, "max results")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	rest := fs.Args()
	if len(rest) < 2 {
		fmt.Fprintln(os.Stderr, "usage: scan <cf> <start-key> [-limit N]")
		os.Exit(1)
	}
	req := &kvrpcpb.KvScanRequest{Cf: rest[0], StartKey: []byte(rest[1]), Limit: uint32(*limit)}
	resp, err := client.KvScan(context.Background(), req)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, kv := range resp.Kvs {
		fmt.Printf("%s : %s\n", kv.Key, kv.Value)
	}
}
