package emulator_test

import (
	"testing"

	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/mockcontroller"
)

// TestTheEmulatorOwnsItsMicrophoneMute checks that the microphone mute is
// the device's and reaches the controller.
//
// Mute is device-sovereign: the device holds the authority and the
// controller is told. The controller mutes its OUTPUT by sending a volume
// of zero, which is a different thing entirely.
func TestTheEmulatorOwnsItsMicrophoneMute(t *testing.T) {
	mock := mockcontroller.New(mockcontroller.Options{})
	defer mock.Close()

	emu := newEmulator(t, mock, "emulator-mute")

	if err := emu.SetMuted(true); err != nil {
		t.Fatalf("muting: %v", err)
	}

	waitFor(t, "the mute report", func() bool {
		return len(mock.MuteStates()) > 0
	})

	states := mock.MuteStates()
	if !states[len(states)-1].Muted {
		t.Error("the controller was told the device was not muted")
	}
	if !emu.Device().State().Snapshot().Muted {
		t.Error("the device's own state does not know it is muted")
	}
}
