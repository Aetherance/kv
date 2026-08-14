// Package security contains the TLS configuration shared by KKV servers and
// clients.
package security

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

// ServerTLSOptions describes the certificate material used by a KKV server
// for mutual TLS.
type ServerTLSOptions struct {
	CertFile string
	KeyFile  string
	CAFile   string
}

// ClientTLSOptions describes how a KKV or Raft client verifies a server and
// authenticates itself with a client certificate.
type ClientTLSOptions struct {
	CAFile     string
	CertFile   string
	KeyFile    string
	ServerName string
}

// ServerTLSConfig builds a mutual TLS server configuration. It returns nil
// when TLS is not configured, preserving the plaintext default for existing
// users.
func ServerTLSConfig(opts ServerTLSOptions) (*tls.Config, error) {
	if opts.CertFile == "" && opts.KeyFile == "" && opts.CAFile == "" {
		return nil, nil
	}
	if opts.CertFile == "" || opts.KeyFile == "" || opts.CAFile == "" {
		return nil, fmt.Errorf("tls-cert, tls-key, and tls-ca are required together")
	}

	certificate, err := tls.LoadX509KeyPair(opts.CertFile, opts.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("load TLS certificate and key: %w", err)
	}

	clientCAs, err := loadCertPool(opts.CAFile)
	if err != nil {
		return nil, fmt.Errorf("load client CA: %w", err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{certificate},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    clientCAs,
		MinVersion:   tls.VersionTLS12,
	}, nil
}

// ClientTLSConfig builds a mutual TLS client configuration that verifies
// servers against either the supplied CA bundle or the host's system roots.
func ClientTLSConfig(opts ClientTLSOptions) (*tls.Config, error) {
	if opts.CertFile == "" || opts.KeyFile == "" {
		return nil, fmt.Errorf("tls-cert and tls-key are required for mutual TLS")
	}

	config := &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: opts.ServerName,
	}
	if opts.CAFile != "" {
		rootCAs, err := loadCertPool(opts.CAFile)
		if err != nil {
			return nil, fmt.Errorf("load server CA: %w", err)
		}
		config.RootCAs = rootCAs
	}
	certificate, err := tls.LoadX509KeyPair(opts.CertFile, opts.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("load TLS certificate and key: %w", err)
	}
	config.Certificates = []tls.Certificate{certificate}
	return config, nil
}

func loadCertPool(path string) (*x509.CertPool, error) {
	pemData, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemData) {
		return nil, fmt.Errorf("%s contains no valid PEM certificates", path)
	}
	return pool, nil
}
