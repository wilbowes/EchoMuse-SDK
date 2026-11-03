package echomuse_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse"
	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/mockcontroller"
	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/protocol"
)

// TestEndToEndVoiceTurn walks the whole path: register, get the mic,
// send audio, receive playback, report a button press, receive a wake.
func TestEndToEndVoiceTurn(t *testing.T) {
	mock := mockcontroller.New(mockcontroller.Options{})
	defer mock.Close()

	dev, err := echomuse.New(echomuse.Options{
		DeviceID: "e2e-device", Version: "1.0.0",
		Capabilities:  []protocol.Capability{protocol.CapMic, protocol.CapSpeaker, protocol.CapButtons},
		ControllerURL: mock.URL(), CredentialsDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}

	spoke := make(chan int, 1)
	dev.OnSpeaker(func(pcm []byte) {
		select {
		case spoke <- len(pcm):
		default:
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go dev.Run(ctx)

	waitFor(t, "registration", func() bool { return dev.State().Snapshot().Approved })

	// The controller asks for the microphone.
	if err := mock.RequestMic(false); err != nil {
		t.Fatalf("RequestMic: %v", err)
	}
	waitFor(t, "the device to be listening", func() bool {
		return dev.State().Snapshot().Listening
	})

	// The device sends audio.
	pcm := make([]byte, 3200)
	for i := 0; i < 5; i++ {
		if err := dev.SendMicAudio(pcm); err != nil {
			t.Fatalf("SendMicAudio: %v", err)
		}
	}
	waitFor(t, "the controller to receive audio", func() bool { return mock.MicFrames() > 0 })

	// The controller plays a response.
	if err := mock.SendPlayback(make([]byte, 4096)); err != nil {
		t.Fatalf("SendPlayback: %v", err)
	}
	select {
	case n := <-spoke:
		if n != 4096 {
			t.Errorf("the device heard %d bytes, want 4096", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the response never arrived")
	}

	// The controller ends the response.
	if err := mock.EndPlayback(); err != nil {
		t.Fatalf("EndPlayback: %v", err)
	}
	waitFor(t, "the device to stop speaking", func() bool {
		return !dev.State().Snapshot().Speaking
	})

	// The device reports a button press.
	if err := dev.SendButton(protocol.ButtonUp, 100, false); err != nil {
		t.Fatalf("SendButton: %v", err)
	}

	// The device reports its own wake.
	if err := dev.SendOWWWake(protocol.OWWWake{Score: 0.9, Threshold: 0.5}); err != nil {
		t.Fatalf("SendOWWWake: %v", err)
	}

	// The controller pushes a configuration.
	if err := mock.PushConfig(protocol.Config{OwwThreshold: 0.6}); err != nil {
		t.Fatalf("PushConfig: %v", err)
	}

	os.Stdout.Sync()
}
