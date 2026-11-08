//go:build !linux

package audio

import (
	"errors"

	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/emulator"
)

func ListDevices() ([]emulator.AudioDevice, []emulator.AudioDevice, error) {
	return nil, nil, errors.New("the emulator audio adapter requires Linux")
}
