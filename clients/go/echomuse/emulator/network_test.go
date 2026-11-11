package emulator_test

import (
	"context"
	"testing"

	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/emulator"
	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/mockcontroller"
	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/protocol"
)

// stubScanner is a network stack the test controls.
type stubScanner struct {
	networks []protocol.WifiNetwork
	scanErr  error
	joinErr  error
	joined   string
}

func (s *stubScanner) Scan() ([]protocol.WifiNetwork, error) {
	if s.scanErr != nil {
		return nil, s.scanErr
	}
	return s.networks, nil
}

func (s *stubScanner) Current() (string, string, *int, error) {
	return "testnet", "", nil, nil
}

func (s *stubScanner) Join(ssid, psk string) error {
	s.joined = ssid
	return s.joinErr
}

// TestTheEmulatorSaysItCannotScan checks that a device with no radio
// answers the controller's scan with an error.
//
// The controller waits for the answer and gives up after a timeout. A
// silent device is indistinguishable from a busy one, and the operator is
// told a scan timed out rather than that the device has no radio.
func TestTheEmulatorSaysItCannotScan(t *testing.T) {
	mock := mockcontroller.New(mockcontroller.Options{})
	defer mock.Close()

	emu, err := emulator.New(emulator.Options{
		DeviceID:       "emulator-noscan",
		ControllerURL:  mock.URL(),
		CredentialsDir: t.TempDir(),
		Audio:          silentAudio{},
		Scanner:        emulator.NoWifiScanner{},
	})
	if err != nil {
		t.Fatalf("building the emulator: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go emu.Run(ctx)

	waitFor(t, "the emulator to register", func() bool {
		return mock.DeviceID() == "emulator-noscan"
	})

	mock.WriteControl(map[string]any{"type": protocol.TypeWifiScan})

	waitFor(t, "the scan result", func() bool {
		return len(mock.WifiScans()) > 0
	})

	if mock.WifiScans()[0].Error == "" {
		t.Error("the answer carried no error, want the reason the scan failed")
	}
}

// TestTheEmulatorAnswersTheNetworkChange checks that the emulator joins
// the network the controller asked for and reports the outcome.
func TestTheEmulatorAnswersTheNetworkChange(t *testing.T) {
	mock := mockcontroller.New(mockcontroller.Options{})
	defer mock.Close()

	scanner := &stubScanner{}
	emu, err := emulator.New(emulator.Options{
		DeviceID:       "emulator-change",
		ControllerURL:  mock.URL(),
		CredentialsDir: t.TempDir(),
		Audio:          silentAudio{},
		Scanner:        scanner,
	})
	if err != nil {
		t.Fatalf("building the emulator: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go emu.Run(ctx)

	waitFor(t, "the emulator to register", func() bool {
		return mock.DeviceID() == "emulator-change"
	})

	mock.WriteControl(protocol.WifiChange{
		Type: protocol.TypeWifiChange,
		SSID: "newnet",
		PSK:  "secret123",
	})

	waitFor(t, "the outcome", func() bool {
		return len(mock.WifiResults()) > 0
	})

	results := mock.WifiResults()
	if !results[0].OK {
		t.Errorf("the outcome was a failure: %s", results[0].Error)
	}
	if results[0].SSID != "newnet" {
		t.Errorf("the network was %q, want newnet", results[0].SSID)
	}
	if scanner.joined != "newnet" {
		t.Errorf("the emulator joined %q, want newnet", scanner.joined)
	}
}

// TestTheEmulatorReportsAFailedNetworkChange checks that a change that
// failed is reported as a failure.
//
// The controller records the outcome and shows it to the operator. A
// device that fails silently leaves the controller waiting.
func TestTheEmulatorReportsAFailedNetworkChange(t *testing.T) {
	mock := mockcontroller.New(mockcontroller.Options{})
	defer mock.Close()

	scanner := &stubScanner{joinErr: context.DeadlineExceeded}
	emu, err := emulator.New(emulator.Options{
		DeviceID:       "emulator-change-fail",
		ControllerURL:  mock.URL(),
		CredentialsDir: t.TempDir(),
		Audio:          silentAudio{},
		Scanner:        scanner,
	})
	if err != nil {
		t.Fatalf("building the emulator: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go emu.Run(ctx)

	waitFor(t, "the emulator to register", func() bool {
		return mock.DeviceID() == "emulator-change-fail"
	})

	mock.WriteControl(protocol.WifiChange{
		Type: protocol.TypeWifiChange,
		SSID: "newnet",
	})

	waitFor(t, "the outcome", func() bool {
		return len(mock.WifiResults()) > 0
	})

	results := mock.WifiResults()
	if results[0].OK {
		t.Error("the outcome was a success, want a failure")
	}
	if results[0].Error == "" {
		t.Error("the outcome carried no error, want the reason it failed")
	}
}
