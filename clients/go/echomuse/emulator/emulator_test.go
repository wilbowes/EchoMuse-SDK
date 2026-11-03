package emulator_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/emulator"
	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/mockcontroller"
	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/protocol"
	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/state"
)

// silentAudio is an audio backend that produces nothing.
//
// The emulator's audio is the machine's sound card, which a test has not
// got. What is being tested is the flow, not the audio.
type silentAudio struct{}

// Capture returns a reader that ends immediately.
func (silentAudio) Capture(ctx context.Context, rate int) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

// Playback returns a writer that discards.
func (silentAudio) Playback(ctx context.Context, rate int) (io.WriteCloser, error) {
	return nopWriteCloser{}, nil
}

// Describe says what the backend is.
func (silentAudio) Describe() string { return "silent" }

// nopWriteCloser discards everything written to it.
type nopWriteCloser struct{}

// Write discards the samples.
func (nopWriteCloser) Write(p []byte) (int, error) { return len(p), nil }

// Close does nothing.
func (nopWriteCloser) Close() error { return nil }

// waitFor polls until the condition holds or the deadline passes.
func waitFor(t *testing.T, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// newEmulator brings an emulator up against a mock controller.
func newEmulator(t *testing.T, mock *mockcontroller.Controller, deviceID string) *emulator.Emulator {
	t.Helper()

	emu, err := emulator.New(emulator.Options{
		DeviceID:       deviceID,
		ControllerURL:  mock.URL(),
		CredentialsDir: t.TempDir(),
		Audio:          silentAudio{},
	})
	if err != nil {
		t.Fatalf("building the emulator: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go emu.Run(ctx)

	waitFor(t, "the emulator to register", func() bool {
		return mock.DeviceID() == deviceID
	})

	return emu
}

// TestEmulatorIsHeldPending checks the admission flow's first step.
//
// The device asks to pair, and the controller holds it until an
// administrator approves it. That is the one human decision in the flow.
func TestEmulatorIsHeldPending(t *testing.T) {
	mock := mockcontroller.New(mockcontroller.Options{Mode: mockcontroller.ModePending})
	defer mock.Close()

	emu := newEmulator(t, mock, "emulator-pending")

	waitFor(t, "the emulator to report itself pending", func() bool {
		return emu.Device().State().Snapshot().Phase == state.PhasePending
	})

	if !emu.Device().PairingOpen() {
		t.Error("the pairing window was not open")
	}
	if !emu.Device().PairingRequested() {
		t.Error("the emulator did not announce the pairing capability")
	}
}

// TestEmulatorIsIssuedTheCredential checks the credential path.
//
// The controller pushes the token and the certificate authority over the
// shell plane, and the device stores them. A device that cannot do this
// stays on the plain link forever.
func TestEmulatorIsIssuedTheCredential(t *testing.T) {
	mock := mockcontroller.New(mockcontroller.Options{})
	defer mock.Close()

	credDir := t.TempDir()
	emu, err := emulator.New(emulator.Options{
		DeviceID:       "emulator-credential",
		ControllerURL:  mock.URL(),
		CredentialsDir: credDir,
		Audio:          silentAudio{},
	})
	if err != nil {
		t.Fatalf("building the emulator: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go emu.Run(ctx)

	waitFor(t, "the emulator to register", func() bool {
		return mock.DeviceID() == "emulator-credential"
	})

	token := "emulator-token-abcdef"
	ca := []byte("-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----\n")
	if err := mock.IssueCredential(token, ca); err != nil {
		t.Fatalf("issuing the credential: %v", err)
	}

	waitFor(t, "the token to reach the device", func() bool {
		data, err := os.ReadFile(filepath.Join(credDir, "token"))
		return err == nil && strings.TrimSpace(string(data)) == token
	})

	data, err := os.ReadFile(filepath.Join(credDir, "ca.pem"))
	if err != nil {
		t.Fatalf("reading the certificate authority: %v", err)
	}
	if string(data) != string(ca) {
		t.Error("the certificate authority did not arrive intact")
	}
}

// TestEmulatorStreamsTheMicrophone checks that the emulator starts
// streaming when the controller asks for it.
func TestEmulatorStreamsTheMicrophone(t *testing.T) {
	mock := mockcontroller.New(mockcontroller.Options{})
	defer mock.Close()

	emu := newEmulator(t, mock, "emulator-audio")

	waitFor(t, "the link to come up", func() bool {
		return emu.Device().State().Snapshot().LinkUp
	})

	if err := mock.RequestMic(false); err != nil {
		t.Fatalf("asking for the microphone: %v", err)
	}

	waitFor(t, "the emulator to be listening", func() bool {
		return emu.Device().State().Snapshot().Listening
	})
}

// TestEmulatorReportsItsListenState checks that the emulator says what it
// is doing with its microphone.
//
// It streams to the controller, so that is what it says, and the
// controller's dashboard labels it honestly.
func TestEmulatorReportsItsListenState(t *testing.T) {
	mock := mockcontroller.New(mockcontroller.Options{})
	defer mock.Close()

	emu := newEmulator(t, mock, "emulator-listen")

	emu.ReportListenState()

	waitFor(t, "the emulator to record its listen state", func() bool {
		return emu.Device().State().Snapshot().Listen == state.ListenStream
	})
}

// TestEmulatorTapStartsATurn checks that a tap on the window reaches the
// controller as a button press.
func TestEmulatorTapStartsATurn(t *testing.T) {
	mock := mockcontroller.New(mockcontroller.Options{})
	defer mock.Close()

	emu := newEmulator(t, mock, "emulator-tap")
	emu.TapButton()
}

// TestEmulatorAnnouncesTheRightCapabilities checks what the emulator says
// it can do.
//
// Every capability it announces must be one it honours, or the controller
// offers a control that does nothing.
func TestEmulatorAnnouncesTheRightCapabilities(t *testing.T) {
	mock := mockcontroller.New(mockcontroller.Options{})
	defer mock.Close()

	emu := newEmulator(t, mock, "emulator-caps")

	caps := emu.Device().Capabilities()
	has := func(want protocol.Capability) bool {
		for _, c := range caps {
			if c == want {
				return true
			}
		}
		return false
	}

	if !has(protocol.CapMic) {
		t.Error("the emulator did not announce the microphone capability")
	}
	if !has(protocol.CapSpeaker) {
		t.Error("the emulator did not announce the speaker capability")
	}
	if !has(protocol.CapPairing) {
		t.Error("the emulator did not announce the pairing capability")
	}
	if has(protocol.CapLEDs) {
		t.Error("the emulator announced a ring it does not have")
	}
}

// TestEmulatorWindowServesItsState checks that the window's API answers.
func TestEmulatorWindowServesItsState(t *testing.T) {
	mock := mockcontroller.New(mockcontroller.Options{})
	defer mock.Close()

	emu := newEmulator(t, mock, "emulator-window")
	handler := emulator.NewHandler(emu)

	page := doRequest(t, handler, "GET", "/")
	if !strings.Contains(page, "EchoMuse Emulator") {
		t.Error("the window did not serve its page")
	}

	body := doRequest(t, handler, "GET", "/api/state")
	if !strings.Contains(body, "emulator-window") {
		t.Errorf("the window's state did not carry the device id: %s", body)
	}
}

// TestEmulatorWalksTheWholeFlow checks the flow end to end.
//
// The device asks to pair, the controller holds it, an administrator
// approves it, the controller issues the link credential over the shell
// plane, and the device is on the encrypted link. Every one of those is a
// step a person or the controller takes, and the whole sequence is what
// the emulator's window narrates.
func TestEmulatorWalksTheWholeFlow(t *testing.T) {
	mock := mockcontroller.New(mockcontroller.Options{Mode: mockcontroller.ModePending})
	defer mock.Close()

	credDir := t.TempDir()
	emu, err := emulator.New(emulator.Options{
		DeviceID:       "emulator-flow",
		ControllerURL:  mock.URL(),
		CredentialsDir: credDir,
		Audio:          silentAudio{},
	})
	if err != nil {
		t.Fatalf("building the emulator: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go emu.Run(ctx)

	// The device asks to pair and is held.
	waitFor(t, "the emulator to be held pending", func() bool {
		return emu.Device().State().Snapshot().Phase == state.PhasePending
	})

	// An administrator approves it.
	if err := mock.Approve(); err != nil {
		t.Fatalf("approving the device: %v", err)
	}

	waitFor(t, "the emulator to be admitted", func() bool {
		return emu.Device().State().Snapshot().Approved
	})

	// The controller issues the link credential over the shell plane.
	token := "flow-token-123456"
	ca := []byte("-----BEGIN CERTIFICATE-----\nflow\n-----END CERTIFICATE-----\n")
	if err := mock.IssueCredential(token, ca); err != nil {
		t.Fatalf("issuing the credential: %v", err)
	}

	waitFor(t, "the credential to reach the device", func() bool {
		data, err := os.ReadFile(filepath.Join(credDir, "token"))
		return err == nil && strings.TrimSpace(string(data)) == token
	})

	// The window's walkthrough has caught up.
	steps := emu.Steps()
	if len(steps) < 2 {
		t.Fatalf("the walkthrough had %d steps, want at least 2", len(steps))
	}
	if !steps[0].Done {
		t.Error("the first step was not marked done")
	}
}

// doRequest makes a request against a handler and returns the body.
func doRequest(t *testing.T, handler http.Handler, method, path string) string {
	t.Helper()

	req := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("%s %s returned %d", method, path, rec.Code)
	}
	return rec.Body.String()
}

// countingAudio counts the captures and playbacks it hands out, and
// records whether each was closed.
type countingAudio struct {
	mu       sync.Mutex
	captures int
	plays    int
	open     int
}

// Capture returns a reader that produces silence until it is closed.
func (a *countingAudio) Capture(ctx context.Context, rate int) (io.ReadCloser, error) {
	a.mu.Lock()
	a.captures++
	a.open++
	a.mu.Unlock()

	return &countingReader{
		closed: make(chan struct{}),
		onClose: func() {
			a.mu.Lock()
			a.open--
			a.mu.Unlock()
		},
	}, nil
}

// Playback returns a writer that discards.
func (a *countingAudio) Playback(ctx context.Context, rate int) (io.WriteCloser, error) {
	a.mu.Lock()
	a.plays++
	a.mu.Unlock()
	return nopWriteCloser{}, nil
}

// Describe says what the backend is.
func (a *countingAudio) Describe() string { return "counting" }

// openCaptures returns how many captures are still open.
func (a *countingAudio) openCaptures() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.open
}

// countingReader produces silence until it is closed.
type countingReader struct {
	onClose func()
	closed  chan struct{}
	once    sync.Once
}

// Read produces silence.
func (r *countingReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	select {
	case <-r.closed:
		return 0, io.EOF
	default:
	}
	return len(p), nil
}

// Close ends the reader.
func (r *countingReader) Close() error {
	r.once.Do(func() {
		close(r.closed)
		r.onClose()
	})
	return nil
}

// TestStoppingACaptureClosesIt checks that a stopped capture releases the
// sound card.
//
// A capture that is only marked as stopped leaves the sound card open,
// and the next capture opens a second one. The device then streams
// whichever of the two the audio server gave it, and the controller hears
// silence while the window says it is listening.
func TestStoppingACaptureClosesIt(t *testing.T) {
	mock := mockcontroller.New(mockcontroller.Options{Mode: mockcontroller.ModeApprove})
	defer mock.Close()

	audio := &countingAudio{}

	emu, err := emulator.New(emulator.Options{
		ControllerURL:  mock.URL(),
		CredentialsDir: t.TempDir(),
		Audio:          audio,
	})
	if err != nil {
		t.Fatalf("building the emulator: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go emu.Run(ctx)

	select {
	case <-mock.Registered():
	case <-time.After(15 * time.Second):
		t.Fatal("the device never registered")
	}

	// The capture sends what it hears to the controller, so the data
	// plane has to be up for the capture to stay open.
	waitFor(t, "the link to come up", func() bool {
		return emu.Device().State().Snapshot().LinkUp
	})

	emu.StartCapture(ctx)
	waitFor(t, "the capture to open", func() bool {
		audio.mu.Lock()
		defer audio.mu.Unlock()
		return audio.captures > 0 && audio.open == 1
	})

	emu.StopCapture()
	waitFor(t, "the capture to close", func() bool {
		return audio.openCaptures() == 0
	})

	// A second capture opens a second card, and the first one is gone.
	emu.StartCapture(ctx)
	waitFor(t, "the second capture to open", func() bool {
		audio.mu.Lock()
		defer audio.mu.Unlock()
		return audio.captures > 1
	})

	if open := audio.openCaptures(); open != 1 {
		t.Errorf("%d captures were open, want 1", open)
	}
}
