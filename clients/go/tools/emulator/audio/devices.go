//go:build linux

package audio

import (
	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/audiodevice"
	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/emulator"
)

const (
	DeviceDefault = audiodevice.DeviceDefault
	FormatALSA    = audiodevice.FormatALSA
)

// Device is one endpoint, as the kernel and the mixer describe it.
type Device = audiodevice.Device

// ParseDevice turns a name into the device it refers to.
func ParseDevice(raw string) Device { return audiodevice.ParseDevice(raw) }

// ListDevices returns the capture and playback endpoints on this machine.
//
// The listing lives in the SDK core so that every program lists the
// devices the same way, rather than each one having its own copy.
func ListDevices() (inputs, outputs []emulator.AudioDevice, err error) {
	in, out, err := audiodevice.List()
	if err != nil {
		return nil, nil, err
	}
	for _, d := range in {
		inputs = append(inputs, emulator.AudioDevice{Name: d.Name, Description: d.Description, Default: d.Default})
	}
	for _, d := range out {
		outputs = append(outputs, emulator.AudioDevice{Name: d.Name, Description: d.Description, Default: d.Default})
	}
	return inputs, outputs, nil
}
