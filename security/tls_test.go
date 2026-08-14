package security

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestServerTLSConfigDisabled(t *testing.T) {
	config, err := ServerTLSConfig(ServerTLSOptions{})
	if err != nil {
		t.Fatalf("ServerTLSConfig() error = %v", err)
	}
	if config != nil {
		t.Fatal("ServerTLSConfig() returned a config when TLS is disabled")
	}
}

func TestTLSOptionsValidation(t *testing.T) {
	tests := []struct {
		name string
		call func() error
	}{
		{
			name: "server certificate without key",
			call: func() error {
				_, err := ServerTLSConfig(ServerTLSOptions{CertFile: "server.pem"})
				return err
			},
		},
		{
			name: "server CA without certificate",
			call: func() error {
				_, err := ServerTLSConfig(ServerTLSOptions{CAFile: "ca.pem"})
				return err
			},
		},
		{
			name: "client certificate without key",
			call: func() error {
				_, err := ClientTLSConfig(ClientTLSOptions{CertFile: "client.pem"})
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.call(); err == nil {
				t.Fatal("expected invalid TLS options to return an error")
			}
		})
	}
}

func TestMutualTLSHandshake(t *testing.T) {
	dir := t.TempDir()
	caCert, caKey, caFile := createCA(t, dir, "ca")
	serverCert, serverKey := createCertificate(t, dir, "server", caCert, caKey, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, []string{"server.test"})
	clientCert, clientKey := createCertificate(t, dir, "client", caCert, caKey, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, nil)

	serverConfig, err := ServerTLSConfig(ServerTLSOptions{
		CertFile: serverCert,
		KeyFile:  serverKey,
		CAFile:   caFile,
	})
	if err != nil {
		t.Fatalf("ServerTLSConfig() error = %v", err)
	}
	clientConfig, err := ClientTLSConfig(ClientTLSOptions{
		CAFile:     caFile,
		CertFile:   clientCert,
		KeyFile:    clientKey,
		ServerName: "server.test",
	})
	if err != nil {
		t.Fatalf("ClientTLSConfig() error = %v", err)
	}
	assertHandshake(t, serverConfig, clientConfig, true)

	t.Run("missing client certificate", func(t *testing.T) {
		unauthenticatedClient := clientConfig.Clone()
		unauthenticatedClient.Certificates = nil
		assertHandshake(t, serverConfig, unauthenticatedClient, false)
	})

	t.Run("hostname mismatch", func(t *testing.T) {
		wrongNameConfig, err := ClientTLSConfig(ClientTLSOptions{
			CAFile:     caFile,
			CertFile:   clientCert,
			KeyFile:    clientKey,
			ServerName: "other.test",
		})
		if err != nil {
			t.Fatalf("ClientTLSConfig() error = %v", err)
		}
		assertHandshake(t, serverConfig, wrongNameConfig, false)
	})

	t.Run("untrusted CA", func(t *testing.T) {
		_, _, otherCAFile := createCA(t, dir, "other-ca")
		untrustedConfig, err := ClientTLSConfig(ClientTLSOptions{
			CAFile:     otherCAFile,
			CertFile:   clientCert,
			KeyFile:    clientKey,
			ServerName: "server.test",
		})
		if err != nil {
			t.Fatalf("ClientTLSConfig() error = %v", err)
		}
		assertHandshake(t, serverConfig, untrustedConfig, false)
	})
}

func assertHandshake(t *testing.T, serverConfig, clientConfig *tls.Config, wantSuccess bool) {
	t.Helper()
	serverSide, clientSide := net.Pipe()
	deadline := time.Now().Add(2 * time.Second)
	_ = serverSide.SetDeadline(deadline)
	_ = clientSide.SetDeadline(deadline)

	serverDone := make(chan error, 1)
	go func() {
		serverDone <- tls.Server(serverSide, serverConfig).Handshake()
	}()
	clientErr := tls.Client(clientSide, clientConfig).Handshake()
	_ = clientSide.Close()
	serverErr := <-serverDone
	_ = serverSide.Close()

	if wantSuccess && (clientErr != nil || serverErr != nil) {
		t.Fatalf("TLS handshake failed: client error = %v, server error = %v", clientErr, serverErr)
	}
	if !wantSuccess && clientErr == nil && serverErr == nil {
		t.Fatal("TLS handshake unexpectedly succeeded")
	}
}

func createCA(t *testing.T, dir, name string) (*x509.Certificate, *ecdsa.PrivateKey, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          randomSerial(t),
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create CA certificate: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse CA certificate: %v", err)
	}
	certFile := filepath.Join(dir, name+".pem")
	writePEM(t, certFile, "CERTIFICATE", der, 0o644)
	return cert, key, certFile
}

func createCertificate(t *testing.T, dir, name string, ca *x509.Certificate, caKey *ecdsa.PrivateKey, usages []x509.ExtKeyUsage, dnsNames []string) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate %s key: %v", name, err)
	}
	template := &x509.Certificate{
		SerialNumber: randomSerial(t),
		Subject:      pkix.Name{CommonName: name},
		DNSNames:     dnsNames,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  usages,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create %s certificate: %v", name, err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal %s key: %v", name, err)
	}
	certFile := filepath.Join(dir, name+".pem")
	keyFile := filepath.Join(dir, name+"-key.pem")
	writePEM(t, certFile, "CERTIFICATE", der, 0o644)
	writePEM(t, keyFile, "PRIVATE KEY", keyDER, 0o600)
	return certFile, keyFile
}

func randomSerial(t *testing.T) *big.Int {
	t.Helper()
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		t.Fatalf("generate certificate serial: %v", err)
	}
	return serial
}

func writePEM(t *testing.T, path, blockType string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: data}), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
