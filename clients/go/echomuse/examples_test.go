package echomuse_test

import (
	"context"
	"testing"
	"time"

	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse"
	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/mockcontroller"
	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/protocol"
)

// The examples in the documentation are checked here, because an example
// that does not compile is worse than no example.

// TestTheDocumentedDeviceCompiles builds the device the way the guides
// show it.
func TestTheDocumentedDeviceCompiles(t *testing.T) {
	mock := mockcontroller.New(mockcontroller.Options{})
	defer mock.Close()

	device, err := echomuse.New(echomuse.Options{
		DeviceID:        "panel-0001",
		Version:         "0.1.0",
		FirmwareVersion: "v999.0.0",
		Board:           "home-panel",
		BootID:          "0123456789abcdef0123456789abcdef",
		BootReason:      "power_key",
		KernelArch:      "aarch64",
		KernelRelease:   "6.1.0",
		EMMC: map[string]any{
			"rev": 7, "preEol": 1, "lifeA": 1, "lifeB": 2,
			"name": "FJ25AB", "date": "08/2017", "manfid": "0x000015",
		},
		Capabilities:   []protocol.Capability{protocol.CapMic, protocol.CapSpeaker},
		ControllerURL:  mock.URL(),
		CredentialsDir: t.TempDir(),
		StatsInterval:  30 * time.Second,
		StatsSource: func() protocol.Stats {
			return protocol.Stats{
				CPUPct:     12.5,
				MemUsedMb:  180,
				MemTotalMb: 1024,
			}
		},
	})
	if err != nil {
		t.Fatalf("building the device: %v", err)
	}

	// The documented callbacks.
	device.OnMicMute(func(muted bool) {})
	device.OnWifiScan(func() {})
	device.OnWifiChange(func(change protocol.WifiChange) {})
	device.OnWifiCommit(func() {})

	// The documented outbound messages.
	_ = device.SendMicMute(true)
	_ = device.SendWifiScanResult(protocol.WifiScanResult{})
	_ = device.SendWifiResult(protocol.WifiResult{})
	_ = device.SendListenState(protocol.ListenDegraded, "no model")

	// The documented reading.
	_ = device.Stats()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go device.Run(ctx)

	waitFor(t, "the registration", func() bool {
		return mock.Register().DeviceID == "panel-0001"
	})
}
