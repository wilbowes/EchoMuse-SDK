// Package transport finds the controller and dials it.
//
// Discovery is mDNS: the controller advertises `_emcontroller._tcp.local`
// and the device dials out to it. A static address is also accepted, for a
// controller on another subnet where mDNS does not reach, and for a test
// harness.
//
// The transport owns two things the application should not have to think
// about: where the controller is, and how the link is secured. The link
// credential is a header on every connection, and it must be on all three
// planes — a device that sends it on one and not the others is refused on
// the ones that lack it, once the controller has seen it present.
package transport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/grandcat/zeroconf"
)

// ServiceType is the mDNS service the controller advertises.
const ServiceType = "_emcontroller._tcp"

// DefaultTLSServerName is the DNS name the controller's certificate carries.
//
// It is a fixed name and never an IP address: a certificate naming an IP
// would have to be reissued every time the controller's address changed,
// and the device would then be unable to verify it.
const DefaultTLSServerName = "echomuse-controller"

// Controller describes a controller the device can dial.
type Controller struct {
	// Host is the address to dial.
	Host string
	// Port is the plain-WebSocket port.
	Port int
	// TLSPort is the encrypted-WebSocket port. Zero means there is none,
	// and the device must dial plain.
	TLSPort int
	// Name is the mDNS instance name.
	Name string
}

// URL returns the plain WebSocket base URL.
func (c Controller) URL() string {
	return fmt.Sprintf("ws://%s", net.JoinHostPort(c.Host, itoa(c.Port)))
}

// TLSURL returns the encrypted WebSocket base URL.
func (c Controller) TLSURL() string {
	return fmt.Sprintf("wss://%s", net.JoinHostPort(c.Host, itoa(c.TLSPort)))
}

// HasTLS reports whether the controller offers an encrypted port.
func (c Controller) HasTLS() bool { return c.TLSPort > 0 }

// Credentials are the device's link credentials.
//
// The token is the device's secret, and the certificate authority is what
// lets the device verify that the controller is the controller. Both are
// issued by the controller and pushed to the device over the shell plane;
// neither is in the SDK, and neither should be in an application.
type Credentials struct {
	// Token is the value sent as the X-EM-Token header.
	Token string
	// CAPEM is the controller's certificate authority, PEM-encoded. When
	// it is set the device dials only wss, and never sends its token over
	// a plain connection.
	CAPEM []byte
}

// DefaultCredentialsDir is where the credentials live on disk.
const DefaultCredentialsDir = "/data/local/etc/echomuse"

// LoadCredentials reads the credentials from a directory.
//
// The files are re-read on every dial rather than cached, so a credential
// the controller pushes takes effect on the next reconnect without the
// device being restarted.
func LoadCredentials(dir string) (Credentials, error) {
	if dir == "" {
		dir = DefaultCredentialsDir
	}
	var creds Credentials

	token, err := os.ReadFile(filepath.Join(dir, "token"))
	if err != nil {
		return creds, err
	}
	creds.Token = strings.TrimSpace(string(token))

	ca, err := os.ReadFile(filepath.Join(dir, "ca.pem"))
	if err != nil {
		// A token without a certificate authority is a device that has
		// been issued a token but not yet the CA. It is an ordinary
		// intermediate state, not a failure.
		return creds, nil
	}
	creds.CAPEM = ca
	return creds, nil
}

// TLSConfig builds the TLS configuration for a dial.
//
// The server name is fixed, not derived from the address: the controller's
// certificate names the controller, and a certificate naming an IP address
// would be reissued every time the address changed.
//
// The certificate is verified against the controller's own certificate
// authority. There is no system trust store in the path, because the
// controller is on the local network and is not publicly trusted.
func (c Credentials) TLSConfig(serverName string) (*tls.Config, error) {
	if len(c.CAPEM) == 0 {
		return nil, fmt.Errorf("echomuse: no certificate authority to verify the controller against")
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(c.CAPEM) {
		return nil, fmt.Errorf("echomuse: the certificate authority could not be parsed")
	}
	if serverName == "" {
		serverName = DefaultTLSServerName
	}
	return &tls.Config{
		RootCAs:    pool,
		ServerName: serverName,
		MinVersion: tls.VersionTLS12,
	}, nil
}

// Discover finds the controller by mDNS.
//
// It browses for the service and returns the first responder that answers
// on the port it advertised. Verifying it costs one TCP connection and
// saves dialling a stale entry that a network with more than one device on
// it will eventually offer.
//
// The browse is bounded by the context. A network with no controller on it
// is not an error the caller can do anything about, so the caller decides
// what to do with an empty result.
func Discover(ctx context.Context, timeout time.Duration) (Controller, error) {
	entries := make(chan *zeroconf.ServiceEntry, 4)
	browseCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	resolver, err := zeroconf.NewResolver(nil)
	if err != nil {
		return Controller{}, fmt.Errorf("echomuse: starting the mDNS resolver: %w", err)
	}

	if err := resolver.Browse(browseCtx, ServiceType, "local.", entries); err != nil {
		return Controller{}, fmt.Errorf("echomuse: browsing for %s: %w", ServiceType, err)
	}

	for {
		select {
		case entry, ok := <-entries:
			if !ok {
				return Controller{}, fmt.Errorf("echomuse: no controller found on the network")
			}
			if entry == nil {
				continue
			}
			c, ok := controllerFromEntry(entry)
			if !ok {
				continue
			}
			if !reachable(c.Host, c.Port) {
				continue
			}
			return c, nil
		case <-browseCtx.Done():
			return Controller{}, fmt.Errorf("echomuse: no controller found within %s", timeout)
		}
	}
}

// reachable reports whether something answers on the address.
func reachable(host string, port int) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, itoa(port)), 500*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// controllerFromEntry turns an mDNS entry into a controller.
//
// The TXT record carries the optional encrypted port. Its absence means a
// controller older than the TLS feature, which has only the plain port.
func controllerFromEntry(entry *zeroconf.ServiceEntry) (Controller, bool) {
	if entry == nil || len(entry.AddrIPv4) == 0 {
		return Controller{}, false
	}

	c := Controller{
		Host: entry.AddrIPv4[0].String(),
		Port: entry.Port,
		Name: entry.Instance,
	}

	for _, txt := range entry.Text {
		key, value, found := strings.Cut(txt, "=")
		if !found {
			continue
		}
		if key == "tls_port" {
			if p, err := parsePort(value); err == nil {
				c.TLSPort = p
			}
		}
	}

	return c, true
}

// StaticController builds a controller from an address, for a controller
// that mDNS cannot reach.
//
// A TLS port of zero means the controller offers no encrypted port, and the
// device dials plain.
func StaticController(host string, port, tlsPort int) Controller {
	return Controller{Host: host, Port: port, TLSPort: tlsPort}
}

// parsePort reads a port from a TXT record.
func parsePort(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("echomuse: empty port")
	}
	var p int
	if _, err := fmt.Sscanf(s, "%d", &p); err != nil {
		return 0, err
	}
	if p < 1 || p > 65535 {
		return 0, fmt.Errorf("echomuse: port %d out of range", p)
	}
	return p, nil
}

// itoa avoids pulling strconv in for one call.
func itoa(n int) string {
	return fmt.Sprintf("%d", n)
}
