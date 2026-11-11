package emulator_test

import (
	"context"
	"testing"
	"time"

	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/emulator"
	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/mockcontroller"
)

// The emulator's own tests, for the behaviour it demonstrates: the
// telemetry it reports, the boot facts it declares, the network it
// answers for, and the microphone mute it owns.

// TestTheEmulatorReportsItsBootFacts checks that the emulator says what
// it is at registration.
//
// The controller keeps a row per boot, so a device that does not send its
// boot id cannot tell the controller that it restarted rather than
// reconnected.
func TestTheEmulatorReportsItsBootFacts(t *testing.T) {
	mock := mockcontroller.New(mockcontroller.Options{})
	defer mock.Close()

	emu, err := emulator.New(emulator.Options{
		DeviceID:       "emulator-boot",
		ControllerURL:  mock.URL(),
		CredentialsDir: t.TempDir(),
		Audio:          silentAudio{},
		BootID:         "abcdef0123456789abcdef0123456789",
		BootReason:     "power_key",
	})
	if err != nil {
		t.Fatalf("building the emulator: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go emu.Run(ctx)

	waitFor(t, "the emulator to register", func() bool {
		return mock.DeviceID() == "emulator-boot"
	})

	reg := mock.Register()
	if reg.BootID != "abcdef0123456789abcdef0123456789" {
		t.Errorf("the boot id was %q, want the one the emulator was given", reg.BootID)
	}
	if reg.BootReason != "power_key" {
		t.Errorf("the boot reason was %q, want power_key", reg.BootReason)
	}
}

// TestTheEmulatorReportsAVersionAheadOfEveryRelease checks that the
// emulator is not offered a firmware update.
//
// The controller compares a device's reported version against the
// published releases and offers an update when the device is behind. The
// emulator cannot take one — it is not an Echo and has no update slot —
// so it reports a version ahead of every release.
func TestTheEmulatorReportsAVersionAheadOfEveryRelease(t *testing.T) {
	mock := mockcontroller.New(mockcontroller.Options{})
	defer mock.Close()

	emu, err := emulator.New(emulator.Options{
		DeviceID:       "emulator-firmware",
		ControllerURL:  mock.URL(),
		CredentialsDir: t.TempDir(),
		Audio:          silentAudio{},
	})
	if err != nil {
		t.Fatalf("building the emulator: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go emu.Run(ctx)

	waitFor(t, "the emulator to register", func() bool {
		return mock.DeviceID() == "emulator-firmware"
	})

	if got := mock.Register().Version; got != "v999.0.0" {
		t.Errorf("the reported version was %q, want v999.0.0", got)
	}
}

// TestTheEmulatorReportsItsTelemetry checks that the emulator's
// measurements reach the controller.
func TestTheEmulatorReportsItsTelemetry(t *testing.T) {
	mock := mockcontroller.New(mockcontroller.Options{})
	defer mock.Close()

	totalMb, usedMb := emulator.ReadStorage("/")
	emu, err := emulator.New(emulator.Options{
		DeviceID:       "emulator-telemetry",
		ControllerURL:  mock.URL(),
		CredentialsDir: t.TempDir(),
		Audio:          silentAudio{},
		StatsInterval:  100 * time.Millisecond,
		Telemetry: emulator.Telemetry{
			StorageTotalMb: totalMb,
			StorageUsedMb:  usedMb,
			WiFiSSID:       "testnet",
			EMMC:           map[string]any{"lifeA": 1},
		},
	})
	if err != nil {
		t.Fatalf("building the emulator: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go emu.Run(ctx)

	waitFor(t, "the emulator to register", func() bool {
		return mock.DeviceID() == "emulator-telemetry"
	})

	waitFor(t, "the telemetry", func() bool {
		return len(mock.Stats()) > 0
	})

	stats := mock.Stats()[0]
	if stats.WifiSsid != "testnet" {
		t.Errorf("the SSID was %q, want testnet", stats.WifiSsid)
	}
	if stats.CPUPct < 0 || stats.CPUPct > 100 {
		t.Errorf("the CPU share was %v, want a share", stats.CPUPct)
	}
	if stats.CoresTotal == nil || *stats.CoresTotal < 1 {
		t.Errorf("the core count was %v, want at least one", stats.CoresTotal)
	}
	if stats.MemTotalMb <= 0 {
		t.Errorf("the memory total was %v, want a number", stats.MemTotalMb)
	}
}
