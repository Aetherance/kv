package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/Aetherance/kv/proto/pkg/kvpb"
	"github.com/Aetherance/kv/security"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	addr := flag.String("a", ":65535", "server address")
	tlsCA := flag.String("tls-ca", "", "trusted server CA PEM file (enables mutual TLS)")
	tlsCert := flag.String("tls-cert", "", "client certificate PEM file (enables mutual TLS)")
	tlsKey := flag.String("tls-key", "", "client private key PEM file (enables mutual TLS)")
	tlsServerName := flag.String("tls-server-name", "", "server name used for certificate verification")
	flag.Parse()

	if flag.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "usage: kvctl -a <addr> <command> [args...]\n")
		os.Exit(1)
	}

	var transportCredentials credentials.TransportCredentials = insecure.NewCredentials()
	tlsEnabled := *tlsCA != "" || *tlsCert != "" || *tlsKey != "" || *tlsServerName != ""
	if tlsEnabled {
		tlsConfig, err := security.ClientTLSConfig(security.ClientTLSOptions{
			CAFile:     *tlsCA,
			CertFile:   *tlsCert,
			KeyFile:    *tlsKey,
			ServerName: *tlsServerName,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "configure TLS: %v\n", err)
			os.Exit(1)
		}
		transportCredentials = credentials.NewTLS(tlsConfig)
	}

	conn, err := grpc.NewClient(*addr, grpc.WithTransportCredentials(transportCredentials))
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	client := kvpb.NewKvClient(conn)

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
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		os.Exit(1)
	}
}
