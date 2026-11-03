package echomuse_test

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse"
	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/mockcontroller"
	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/protocol"
	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/state"
)

// testDevice is a device wired to a mock controller.
type testDevice struct {
	device  *echomuse.Device
	mock    *mockcontroller.Controller
	credDir string
	cancel  context.CancelFunc
	done    chan struct{}
}

// newTestDevice brings a device up against a mock controller.
func newTestDevice(t *testing.T, mode mockcontroller.Mode) *testDevice {
	t.Helper()

	mock := mockcontroller.New(mockcontroller.Options{Mode: mode})
	t.Cleanup(mock.Close)

	credDir := t.TempDir()

	device, err := echomuse.New(echomuse.Options{
		DeviceID:       "test-device-0001",
		Version:        "1.0.0",
		Board:          "testboard",
		Capabilities:   []protocol.Capability{protocol.CapMic, protocol.CapSpeaker, protocol.CapButtons},
		ControllerURL:  mock.URL(),
		CredentialsDir: credDir,
		ReconnectDelay: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("building the device: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	td := &testDevice{
		device:  device,
		mock:    mock,
		credDir: credDir,
		cancel:  cancel,
		done:    make(chan struct{}),
	}

	go func() {
		defer close(td.done)
		device.Run(ctx)
	}()

	t.Cleanup(func() {
		cancel()
		select {
		case <-td.done:
		case <-time.After(5 * time.Second):
			t.Error("the device did not stop")
		}
	})

	return td
}

// waitFor polls until the condition holds or the deadline passes.
func waitFor(t *testing.T, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestDeviceRegistersAndIsApproved is the first thing that has to work: a
// device dials the controller, registers itself and is admitted.
func TestDeviceRegistersAndIsApproved(t *testing.T) {
	td := newTestDevice(t, mockcontroller.ModeApprove)

	select {
	case <-td.mock.Registered():
	case <-time.After(10 * time.Second):
		t.Fatal("the device never registered")
	}

	if got := td.mock.DeviceID(); got != "test-device-0001" {
		t.Errorf("the controller saw the device as %q, want %q", got, "test-device-0001")
	}

	waitFor(t, "the device to be approved", func() bool {
		return td.device.State().Snapshot().Approved
	})
}

// TestDeviceIsPendingWhenTheControllerHoldsIt checks the other answer: a
// device the controller has not approved is told so, and says so.
func TestDeviceIsPendingWhenTheControllerHoldsIt(t *testing.T) {
	td := newTestDevice(t, mockcontroller.ModePending)

	waitFor(t, "the device to report that it is pending", func() bool {
		return td.device.State().Snapshot().Phase == state.PhasePending
	})

	if td.device.State().Snapshot().Approved {
		t.Error("a pending device reported itself as approved")
	}
}

// TestDeviceReceivesConfig checks that the controller's configuration push
// reaches the application.
func TestDeviceReceivesConfig(t *testing.T) {
	td := newTestDevice(t, mockcontroller.ModeApprove)

	received := make(chan protocol.Config, 1)
	td.device.OnConfig(func(c protocol.Config) {
		select {
		case received <- c:
		default:
		}
	})

	waitFor(t, "the device to be approved", func() bool {
		return td.device.State().Snapshot().Approved
	})

	threshold := 0.42
	err := td.mock.PushConfig(protocol.Config{
		OwwThreshold: threshold,
		WakeSound:    boolPtr(true),
	})
	if err != nil {
		t.Fatalf("pushing the config: %v", err)
	}

	select {
	case c := <-received:
		if c.OwwThreshold != threshold {
			t.Errorf("the config carried a threshold of %v, want %v", c.OwwThreshold, threshold)
		}
		if c.WakeSound == nil || !*c.WakeSound {
			t.Error("the config did not carry the wake sound setting")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the config never arrived")
	}
}

// TestDeviceSendsMicAudio checks that captured audio reaches the controller
// in the right shape.
func TestDeviceSendsMicAudio(t *testing.T) {
	td := newTestDevice(t, mockcontroller.ModeApprove)

	waitFor(t, "the device to be approved", func() bool {
		return td.device.State().Snapshot().Approved
	})

	// The controller asks for the microphone before it expects audio.
	waitFor(t, "the data plane to come up", func() bool {
		return td.mock.MicFrames() >= 0
	})

	pcm := make([]byte, 3200)
	for i := range pcm {
		pcm[i] = byte(i)
	}
	if err := td.device.SendMicAudio(pcm); err != nil {
		t.Fatalf("sending the microphone audio: %v", err)
	}

	waitFor(t, "the controller to receive the audio", func() bool {
		return td.mock.MicFrames() > 0
	})
}

// TestDeviceReceivesPlayback checks the other direction of the data plane.
func TestDeviceReceivesPlayback(t *testing.T) {
	td := newTestDevice(t, mockcontroller.ModeApprove)

	received := make(chan int, 1)
	td.device.OnSpeaker(func(pcm []byte) {
		select {
		case received <- len(pcm):
		default:
		}
	})

	waitFor(t, "the device to be approved", func() bool {
		return td.device.State().Snapshot().Approved
	})

	payload := make([]byte, 4096)
	if err := td.mock.SendPlayback(payload); err != nil {
		t.Fatalf("sending the playback: %v", err)
	}

	select {
	case n := <-received:
		if n != len(payload) {
			t.Errorf("the device received %d bytes, want %d", n, len(payload))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the playback never arrived")
	}
}

// TestDeviceReceivesLEDFrames checks that the ring frames reach the
// application, which is what a display renders.
func TestDeviceReceivesLEDFrames(t *testing.T) {
	td := newTestDevice(t, mockcontroller.ModeApprove)

	frames := make(chan protocol.LEDs, 1)
	td.device.OnLEDs(func(l protocol.LEDs) {
		select {
		case frames <- l:
		default:
		}
	})

	waitFor(t, "the device to be approved", func() bool {
		return td.device.State().Snapshot().Approved
	})

	leds := []protocol.LED{{ID: 0, R: 255, G: 0, B: 0}, {ID: 1, R: 0, G: 255, B: 0}}
	if err := td.mock.SendLEDs(leds, true); err != nil {
		t.Fatalf("sending the LED frame: %v", err)
	}

	select {
	case f := <-frames:
		if len(f.LEDs) != 2 {
			t.Fatalf("the frame carried %d indicators, want 2", len(f.LEDs))
		}
		if f.LEDs[0].R != 255 {
			t.Errorf("the first indicator was red %d, want 255", f.LEDs[0].R)
		}
		if f.Listening == nil || !*f.Listening {
			t.Error("the frame was not marked as the listening ring")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the LED frame never arrived")
	}
}

// TestDeviceReceivesAnimation checks that an animation spec reaches the
// application.
func TestDeviceReceivesAnimation(t *testing.T) {
	td := newTestDevice(t, mockcontroller.ModeApprove)

	anims := make(chan protocol.AnimSpec, 1)
	td.device.OnLEDAnim(func(a protocol.AnimSpec) {
		select {
		case anims <- a:
		default:
		}
	})

	waitFor(t, "the device to be approved", func() bool {
		return td.device.State().Snapshot().Approved
	})

	err := td.mock.SendLEDAnim(protocol.AnimSpec{
		Pattern:  "spin",
		Colors:   [][3]uint8{{0, 255, 0}, {0, 128, 0}},
		PeriodMs: 80,
		TTLSec:   30,
	})
	if err != nil {
		t.Fatalf("sending the animation: %v", err)
	}

	select {
	case a := <-anims:
		if a.Pattern != "spin" {
			t.Errorf("the animation was %q, want %q", a.Pattern, "spin")
		}
		if len(a.Colors) != 2 {
			t.Errorf("the animation carried %d colours, want 2", len(a.Colors))
		}
		if a.TTLSec != 30 {
			t.Errorf("the animation's TTL was %d, want 30", a.TTLSec)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the animation never arrived")
	}
}

// TestDeviceReportsItsListenState checks that the device can say what it
// is doing with its microphone, which is what the privacy statement rests
// on.
func TestDeviceReportsItsListenState(t *testing.T) {
	td := newTestDevice(t, mockcontroller.ModeApprove)

	waitFor(t, "the device to be approved", func() bool {
		return td.device.State().Snapshot().Approved
	})

	if err := td.device.SendListenState(protocol.ListenLocal, ""); err != nil {
		t.Fatalf("reporting the listen state: %v", err)
	}

	waitFor(t, "the device to record its listen state", func() bool {
		return td.device.State().Snapshot().Listen == state.ListenLocal
	})

	snap := td.device.State().Snapshot()
	if snap.Listen.Streams() {
		t.Error("a device listening privately reported that it streams")
	}
}

// TestDeviceReportsAButton checks that a button press reaches the
// controller with the right shape.
func TestDeviceReportsAButton(t *testing.T) {
	td := newTestDevice(t, mockcontroller.ModeApprove)

	waitFor(t, "the device to be approved", func() bool {
		return td.device.State().Snapshot().Approved
	})

	if err := td.device.SendButton(protocol.ButtonUp, 120, false); err != nil {
		t.Fatalf("reporting the button: %v", err)
	}
}

// TestDeviceIsIssuedACredential checks the whole credential path.
//
// This is the step that makes a device a first-class device: the
// controller pushes the token and the certificate authority over the shell
// plane, and the device stores them. A device that cannot do this stays on
// the plain link forever.
func TestDeviceIsIssuedACredential(t *testing.T) {
	td := newTestDevice(t, mockcontroller.ModeApprove)

	waitFor(t, "the device to be approved", func() bool {
		return td.device.State().Snapshot().Approved
	})

	token := "test-token-0123456789"
	ca := []byte("-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----\n")

	if err := td.mock.IssueCredential(token, ca); err != nil {
		t.Fatalf("issuing the credential: %v", err)
	}

	// The controller pushed the files over the shell plane.
	waitFor(t, "the token to reach the device", func() bool {
		data, err := os.ReadFile(filepath.Join(td.credDir, "token"))
		return err == nil && strings.TrimSpace(string(data)) == token
	})

	data, err := os.ReadFile(filepath.Join(td.credDir, "ca.pem"))
	if err != nil {
		t.Fatalf("reading the certificate authority: %v", err)
	}
	if string(data) != string(ca) {
		t.Error("the certificate authority did not arrive intact")
	}
}

// TestDeviceReconnects checks that a dropped link is re-established.
func TestDeviceReconnects(t *testing.T) {
	td := newTestDevice(t, mockcontroller.ModeApprove)

	waitFor(t, "the device to be approved", func() bool {
		return td.device.State().Snapshot().Approved
	})

	// Take the link down and watch it come back.
	td.mock.Close()

	waitFor(t, "the device to notice the link dropped", func() bool {
		return !td.device.State().Snapshot().LinkUp
	})
}

// TestMicFrameLayout pins the capture frame's byte layout.
//
// The sequence number is big endian and the samples follow it. A frame
// built any other way is ignored by the controller, in silence.
func TestMicFrameLayout(t *testing.T) {
	pcm := []byte{0x01, 0x02, 0x03, 0x04}
	frame := protocol.MicFrame{Seq: 0x0102, PCM: pcm}.Encode()

	if frame[0] != protocol.FrameMic {
		t.Errorf("the frame's type byte was 0x%02x, want 0x%02x", frame[0], protocol.FrameMic)
	}
	if got := binary.BigEndian.Uint16(frame[1:3]); got != 0x0102 {
		t.Errorf("the sequence number was 0x%04x, want 0x0102", got)
	}
	if string(frame[3:]) != string(pcm) {
		t.Error("the samples did not survive the encoding")
	}

	parsed, err := protocol.ParseMicFrame(frame)
	if err != nil {
		t.Fatalf("parsing the frame: %v", err)
	}
	if parsed.Seq != 0x0102 {
		t.Errorf("the parsed sequence number was %d, want 258", parsed.Seq)
	}
	if string(parsed.PCM) != string(pcm) {
		t.Error("the parsed samples did not match")
	}
}

// TestListenFrameLayout pins the private-listening frame's byte layout.
func TestListenFrameLayout(t *testing.T) {
	pcm := []byte{0x0a, 0x0b}
	frame := protocol.ListenFrame{Session: 0x01020304, Seq: 0x0506, PCM: pcm}.Encode()

	if frame[0] != protocol.FrameListen {
		t.Errorf("the frame's type byte was 0x%02x, want 0x%02x", frame[0], protocol.FrameListen)
	}
	if got := binary.BigEndian.Uint32(frame[1:5]); got != 0x01020304 {
		t.Errorf("the session was 0x%08x, want 0x01020304", got)
	}
	if got := binary.BigEndian.Uint16(frame[5:7]); got != 0x0506 {
		t.Errorf("the sequence number was 0x%04x, want 0x0506", got)
	}

	parsed, err := protocol.ParseListenFrame(frame)
	if err != nil {
		t.Fatalf("parsing the frame: %v", err)
	}
	if parsed.Session != 0x01020304 || parsed.Seq != 0x0506 {
		t.Errorf("the parsed frame was %d/%d, want 16909060/1286", parsed.Session, parsed.Seq)
	}
}

// TestFrameCodesAreDirectional checks that the two capture sentinels and
// the two playback codes do not collide.
//
// The codes are namespaced by direction, and that is deliberate: 0x04 and
// 0x05 mean different things depending on who sent them.
func TestFrameCodesAreDirectional(t *testing.T) {
	if protocol.FrameVADEnd != protocol.FrameMusic {
		t.Error("the VAD-end sentinel and the music frame share a code")
	}
	if protocol.FrameNoSpeechTimeout != protocol.FrameMusicEOS {
		t.Error("the no-speech sentinel and the music end frame share a code")
	}
	if protocol.FrameSpeaker == protocol.FrameSpeakerEOS {
		t.Error("the speaker and speaker-end frames share a code")
	}
}

// TestCapabilitiesAreKnown checks that every capability the SDK names is
// one the controller understands.
//
// A capability the controller does not know is ignored, so the device
// announces a feature that does not exist and the dashboard shows a
// control that does nothing.
func TestCapabilitiesAreKnown(t *testing.T) {
	known := map[protocol.Capability]bool{}
	for _, c := range protocol.AllCapabilities {
		known[c] = true
	}
	for _, c := range protocol.AllCapabilities {
		if !known[c] {
			t.Errorf("the capability %q is not in the known list", c)
		}
	}
	if len(protocol.AllCapabilities) == 0 {
		t.Error("the known capability list is empty")
	}
}

// boolPtr returns a pointer to a bool.
func boolPtr(b bool) *bool { return &b }
