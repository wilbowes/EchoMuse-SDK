//go:build !linux || !cgo

package audio

import (
	"context"
	"errors"
	"io"

	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/emulator"
)

type unavailable struct{}

func NewBackend(input, output string) emulator.AudioBackend { return unavailable{} }
func (unavailable) Capture(context.Context, int) (io.ReadCloser, error) {
	return nil, errors.New("the emulator audio adapter requires Linux with cgo")
}
func (unavailable) Playback(context.Context, int) (io.WriteCloser, error) {
	return nil, errors.New("the emulator audio adapter requires Linux with cgo")
}
func (unavailable) Describe() string { return "ALSA unavailable (requires Linux with cgo)" }
