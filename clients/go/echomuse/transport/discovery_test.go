package transport_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/transport"
)

// TestControllerURLs checks the two URLs a controller is dialled at.
func TestControllerURLs(t *testing.T) {
	c := transport.StaticController("192.168.1.10", 8767, 8770)

	if got := c.URL(); got != "ws://192.168.1.10:8767" {
		t.Errorf("the plain URL was %q", got)
	}
	if got := c.TLSURL(); got != "wss://192.168.1.10:8770" {
		t.Errorf("the encrypted URL was %q", got)
	}
	if !c.HasTLS() {
		t.Error("a controller with an encrypted port reported that it has none")
	}
}

// TestControllerWithoutTLS checks that a controller with no encrypted port
// says so.
//
// A device holding the certificate authority dials wss only, so it has to
// be able to tell that there is no encrypted port to dial.
func TestControllerWithoutTLS(t *testing.T) {
	c := transport.StaticController("192.168.1.10", 8767, 0)
	if c.HasTLS() {
		t.Error("a controller with no encrypted port reported that it has one")
	}
}

// TestLoadCredentialsReadsTheFiles checks that the credentials are read
// from disk.
//
// They are re-read on every dial rather than cached, so a credential the
// controller pushes takes effect on the next reconnect.
func TestLoadCredentialsReadsTheFiles(t *testing.T) {
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "token"), []byte("  secret-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ca := []byte("-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----\n")
	if err := os.WriteFile(filepath.Join(dir, "ca.pem"), ca, 0o644); err != nil {
		t.Fatal(err)
	}

	creds, err := transport.LoadCredentials(dir)
	if err != nil {
		t.Fatalf("loading the credentials: %v", err)
	}
	if creds.Token != "secret-token" {
		t.Errorf("the token was %q, want it trimmed", creds.Token)
	}
	if string(creds.CAPEM) != string(ca) {
		t.Error("the certificate authority did not survive the round trip")
	}
}

// TestLoadCredentialsWithoutTheCA checks the intermediate state.
//
// A device that has been issued a token but not yet the certificate
// authority is in the middle of being issued credentials. That is an
// ordinary state, not a failure.
func TestLoadCredentialsWithoutTheCA(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "token"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	creds, err := transport.LoadCredentials(dir)
	if err != nil {
		t.Fatalf("loading the credentials: %v", err)
	}
	if creds.Token != "secret" {
		t.Errorf("the token was %q", creds.Token)
	}
	if len(creds.CAPEM) != 0 {
		t.Error("a certificate authority appeared that was not there")
	}
}

// TestLoadCredentialsWithNothingThere checks the unissued state.
func TestLoadCredentialsWithNothingThere(t *testing.T) {
	creds, err := transport.LoadCredentials(t.TempDir())
	if err == nil {
		t.Error("loading from an empty directory succeeded")
	}
	if creds.Token != "" {
		t.Error("a token appeared that was not there")
	}
}

// TestTLSConfigNeedsTheCA checks that a dial without a certificate
// authority is refused.
//
// There is no system trust store in the path, because the controller is on
// the local network and is not publicly trusted.
func TestTLSConfigNeedsTheCA(t *testing.T) {
	var creds transport.Credentials
	if _, err := creds.TLSConfig("echomuse-controller"); err == nil {
		t.Error("a TLS configuration was built without a certificate authority")
	}
}

// TestTLSConfigRejectsABadCA checks that a certificate authority which
// cannot be parsed is an error rather than an empty trust store.
func TestTLSConfigRejectsABadCA(t *testing.T) {
	creds := transport.Credentials{CAPEM: []byte("not a certificate")}
	_, err := creds.TLSConfig("echomuse-controller")
	if err == nil {
		t.Fatal("a broken certificate authority was accepted")
	}
	if !strings.Contains(err.Error(), "certificate authority") {
		t.Errorf("the error was %q, want it to name the certificate authority", err)
	}
}

// TestTLSConfigUsesTheFixedServerName checks that the server name is the
// controller's, not the address.
//
// A certificate naming an IP address would have to be reissued every time
// the controller's address changed.
func TestTLSConfigUsesTheFixedServerName(t *testing.T) {
	creds := transport.Credentials{CAPEM: testCA(t)}

	cfg, err := creds.TLSConfig("")
	if err != nil {
		t.Fatalf("building the TLS configuration: %v", err)
	}
	if cfg.ServerName != transport.DefaultTLSServerName {
		t.Errorf("the server name was %q, want %q", cfg.ServerName, transport.DefaultTLSServerName)
	}
}

// testCA returns a certificate authority that parses.
//
// It is a real self-signed certificate, because the point of the test is
// that the authority is parsed rather than accepted on trust.
func testCA(t *testing.T) []byte {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating a key: %v", err)
	}

	template := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "echomuse-controller"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
	}

	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("creating the certificate: %v", err)
	}

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
