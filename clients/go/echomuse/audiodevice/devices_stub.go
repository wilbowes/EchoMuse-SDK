//go:build !linux

package audiodevice

import "errors"

// List returns nothing, because there is no ALSA off Linux.
//
// The error says so plainly rather than pretending the machine has one
// endpoint, so a program can tell the difference between "there is no sound
// card" and "there is one and it is the default".
func List() (inputs, outputs []Endpoint, err error) {
	return nil, nil, errors.New("audio devices can only be listed on Linux")
}
