package emulator_test

import (
	"context"
	"testing"
	"time"

	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/emulator"
	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/mockcontroller"
)

// TestTheEmulatorTelemetryReachesTheController proves the whole path:
// the emulator measures, the SDK sends, the controller receives.
func TestTheEmulatorTelemetryReachesTheController(t *testing.T) {
	mock := mockcontroller.New(mockcontroller.Options{})
	defer mock.Close()

	emu, err := emulator.New(emulator.Options{
		DeviceID:       "emulator-proof",
		ControllerURL:  mock.URL(),
		CredentialsDir: t.TempDir(),
		Audio:          silentAudio{},
		StatsInterval:  100 * time.Millisecond,
		Telemetry:      emulator.Telemetry{WiFiSSID: "proofnet"},
	})
	if err != nil {
		t.Fatalf("building the emulator: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go emu.Run(ctx)

	waitFor(t, "the emulator to register", func() bool {
		return mock.DeviceID() == "emulator-proof"
	})
	// The first reading has no interval behind it — there is nothing to
	// compare the process's CPU time against — so the first one is zero
	// and the second carries the measurement. A test that wants a number
	// waits for the second.
	waitFor(t, "the telemetry to arrive", func() bool {
		return len(mock.Stats()) > 1
	})

	stats := mock.Stats()[len(mock.Stats())-1]
	t.Logf("the controller received: cpu=%.1f%% mem=%d/%dMB cores=%d/%d ssid=%s rssi=%v",
		stats.CPUPct, stats.MemUsedMb, stats.MemTotalMb,
		*stats.CoresOnline, *stats.CoresTotal, stats.WifiSsid, stats.WifiRssi)
	if stats.WifiSsid != "proofnet" {
		t.Errorf("the SSID was %q, want proofnet", stats.WifiSsid)
	}
	if stats.CPUPct <= 0 {
		t.Errorf("the CPU share was %v, want a positive share", stats.CPUPct)
	}
	if stats.CoresTotal == nil || *stats.CoresTotal < 1 {
		t.Error("the core count was absent")
	}
	// The boot facts the controller keeps a row per boot for.
	if mock.Register().BootID == "" {
		t.Error("the registration carried no boot id")
	}
	if mock.Register().Version != "v999.0.0" {
		t.Errorf("the version was %q, want v999.0.0", mock.Register().Version)
	}
}
