package echomuse_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse"
	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/mockcontroller"
	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/protocol"
)

// TestDataPlaneIdentifiesFirst checks that the data plane's first message
// is an identify.
//
// The controller reads the first frame to learn which device the
// connection is for, and closes a data plane whose first frame is
// anything else — including a microphone frame, which it has no device id
// to route. A device that opens the data plane and starts streaming has
// its connection closed by the controller, in silence.
func TestDataPlaneIdentifiesFirst(t *testing.T) {
	td := newTestDevice(t, mockcontroller.ModeApprove)

	waitFor(t, "the device to be approved", func() bool {
		return td.device.State().Snapshot().Approved
	})

	waitFor(t, "the data plane to identify", func() bool {
		return td.mock.Identified() != ""
	})

	if got := td.mock.Identified(); got != "test-device-0001" {
		t.Errorf("the data plane identified as %q, want %q", got, "test-device-0001")
	}
}

// TestShellCommandsArriveAsText checks that the device reads the shell
// plane's commands.
//
// The controller sends shell commands as text frames. The Echo firmware
// ignores the frame type entirely, and a device that insists on binary
// frames reads nothing: the shell produces no output, the controller
// waits, and the dashboard reports that the device has no shell at all.
func TestShellCommandsArriveAsText(t *testing.T) {
	td := newTestDevice(t, mockcontroller.ModeApprove)

	waitFor(t, "the device to be approved", func() bool {
		return td.device.State().Snapshot().Approved
	})

	// The credential issue pushes files over the shell plane, and the
	// controller sends its commands as text frames. The credential landing
	// on disk is the proof that the text frames were read.
	token := "shell-text-token"
	ca := []byte("-----BEGIN CERTIFICATE-----\ntext\n-----END CERTIFICATE-----\n")

	if err := td.mock.IssueCredential(token, ca); err != nil {
		t.Fatalf("issuing the credential: %v", err)
	}

	waitFor(t, "the token to reach the device", func() bool {
		data, err := os.ReadFile(filepath.Join(td.credDir, "token"))
		return err == nil && strings.TrimSpace(string(data)) == token
	})
}

// TestASecondShellSessionReplacesTheFirst checks that a second shell
// request is served.
//
// The controller runs one shell session per operation — a command, then a
// file transfer — and opens a new one for each. A device that ignores the
// second request leaves the controller waiting on a session that never
// arrives, and it reports that as the device having no shell.
func TestASecondShellSessionReplacesTheFirst(t *testing.T) {
	td := newTestDevice(t, mockcontroller.ModeApprove)

	waitFor(t, "the device to be approved", func() bool {
		return td.device.State().Snapshot().Approved
	})

	// The first credential issue opens a shell session and closes it.
	// The second has to open another one and work just as well.
	for i, token := range []string{"first-token", "second-token"} {
		ca := []byte("-----BEGIN CERTIFICATE-----\n" + token + "\n-----END CERTIFICATE-----\n")
		if err := td.mock.IssueCredential(token, ca); err != nil {
			t.Fatalf("issuing credential %d: %v", i, err)
		}
		want := token
		waitFor(t, "the token to reach the device", func() bool {
			data, err := os.ReadFile(filepath.Join(td.credDir, "token"))
			return err == nil && strings.TrimSpace(string(data)) == want
		})
	}
}

// TestPingWithAnIntegerIDIsAnswered checks the keepalive's reply.
//
// The controller's pings carry a sequence id, and it is an integer. A
// device that expects a string fails to parse the ping, sends no pong,
// and the controller has no round-trip sample to pair with its own send.
func TestPingWithAnIntegerIDIsAnswered(t *testing.T) {
	td := newTestDevice(t, mockcontroller.ModeApprove)

	waitFor(t, "the device to be approved", func() bool {
		return td.device.State().Snapshot().Approved
	})

	// The controller pings with an integer id, and the answer has to
	// carry that id back byte for byte. A device that parses the id as a
	// string fails to answer a ping whose id is a number, and the
	// controller has no round-trip sample to pair with its own send.
	waitFor(t, "a pong carrying the controller's id", func() bool {
		return len(td.mock.PongIDs()) > 0
	})

	for _, id := range td.mock.PongIDs() {
		if strings.HasPrefix(id, "\"") {
			t.Errorf("the id came back as a string (%s), want the number it was sent as", id)
		}
	}
}

// TestAConnectionSurvivesAQuietLink checks the read deadline.
//
// The controller pings every twenty seconds over the WebSocket protocol,
// which gorilla answers by itself. A device whose read deadline is
// refreshed only by a pong therefore has its deadline expire on a
// perfectly healthy link, at exactly one deadline after it was set. The
// link then drops and comes back, which looks like a flaky network.
func TestAConnectionSurvivesAQuietLink(t *testing.T) {
	td := newTestDevice(t, mockcontroller.ModeApprove)

	waitFor(t, "the device to be approved", func() bool {
		return td.device.State().Snapshot().Approved
	})

	// The deadline the SDK uses is longer than the controller's ping
	// interval. A shorter one would close the link on a quiet controller.
	waitFor(t, "the link to stay up", func() bool {
		return td.device.State().Snapshot().LinkUp
	})

	time.Sleep(2 * time.Second)

	if !td.device.State().Snapshot().LinkUp {
		t.Error("the link dropped on a quiet controller")
	}
}

// TestAPairedDeviceDoesNotAskAgain checks that holding a credential ends
// the pairing request.
//
// A device that keeps asking after its approval leaves the dashboard
// showing an approval for a device that is already in the fleet.
func TestAPairedDeviceDoesNotAskAgain(t *testing.T) {
	credDir := t.TempDir()
	ca := []byte("-----BEGIN CERTIFICATE-----\npaired\n-----END CERTIFICATE-----\n")
	if err := os.WriteFile(filepath.Join(credDir, "ca.pem"), ca, 0o644); err != nil {
		t.Fatalf("writing the certificate authority: %v", err)
	}
	if err := os.WriteFile(filepath.Join(credDir, "token"), []byte("paired-token"), 0o600); err != nil {
		t.Fatalf("writing the token: %v", err)
	}

	device, err := echomuse.New(echomuse.Options{
		DeviceID:       "paired-device",
		ControllerURL:  "ws://127.0.0.1:1",
		CredentialsDir: credDir,
		Capabilities:   []protocol.Capability{protocol.CapMic, protocol.CapPairing},
	})
	if err != nil {
		t.Fatalf("building the device: %v", err)
	}

	if !device.Paired() {
		t.Error("a device holding a credential did not report itself as paired")
	}

	device.StartPairing()
	if device.PairingOpen() {
		t.Error("a paired device opened a pairing window")
	}
}

// TestAnUnpairedDeviceAsks checks the other side of the same rule.
func TestAnUnpairedDeviceAsks(t *testing.T) {
	device, err := echomuse.New(echomuse.Options{
		DeviceID:       "unpaired-device",
		ControllerURL:  "ws://127.0.0.1:1",
		CredentialsDir: t.TempDir(),
		Capabilities:   []protocol.Capability{protocol.CapMic, protocol.CapPairing},
	})
	if err != nil {
		t.Fatalf("building the device: %v", err)
	}

	if device.Paired() {
		t.Error("a device with no credential reported itself as paired")
	}

	device.StartPairing()
	if !device.PairingOpen() {
		t.Error("an unpaired device did not open a pairing window")
	}
}

// TestTheEncryptedPortIsDialledWhenOneIsGiven checks that a controller
// named by address can still be joined over TLS.
//
// A controller found by mDNS advertises its encrypted port in the TXT
// record. One named by address does not, so the port has to be given —
// and a device holding the certificate authority dials only the encrypted
// link.
func TestTheEncryptedPortIsDialledWhenOneIsGiven(t *testing.T) {
	credDir := t.TempDir()
	ca := []byte("-----BEGIN CERTIFICATE-----\nnot a real certificate\n-----END CERTIFICATE-----\n")
	if err := os.WriteFile(filepath.Join(credDir, "ca.pem"), ca, 0o644); err != nil {
		t.Fatalf("writing the certificate authority: %v", err)
	}
	if err := os.WriteFile(filepath.Join(credDir, "token"), []byte("tls-token"), 0o600); err != nil {
		t.Fatalf("writing the token: %v", err)
	}

	// A port nothing listens on, so the dial is refused at once rather
	// than hanging.
	refused := listenerPort(t)

	device, err := echomuse.New(echomuse.Options{
		DeviceID:          "tls-device",
		ControllerURL:     "ws://127.0.0.1:" + refused,
		ControllerTLSPort: 8770,
		CredentialsDir:    credDir,
		Capabilities:      []protocol.Capability{protocol.CapMic, protocol.CapPairing},
		ReconnectDelay:    10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("building the device: %v", err)
	}

	// The address is refused, so the dial fails — but the error names the
	// encrypted port, which is the thing being checked. A device that
	// dialled the plain port would name the other one.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	go device.Run(ctx)

	waitFor(t, "the dial to fail on the encrypted port", func() bool {
		return strings.Contains(device.State().Snapshot().LastError, "8770")
	})
}

// listenerPort returns a port nothing is listening on.
//
// The listener is opened and closed again, so the port is one that was
// free a moment ago. A dial to it is refused at once.
func listenerPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("opening a listener: %v", err)
	}
	addr := ln.Addr().(*net.TCPAddr)
	ln.Close()
	return strconv.Itoa(addr.Port)
}
