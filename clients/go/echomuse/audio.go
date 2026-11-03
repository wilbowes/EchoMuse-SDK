package echomuse

import (
	"context"
	"io"
	"time"
)

// AudioBackend is the system supplied PCM input and output used by an
// application. Samples are signed 16-bit little-endian PCM at the requested
// rate. The SDK does not select a platform audio system or device.
type AudioBackend interface {
	Capture(context.Context, int) (io.ReadCloser, error)
	Playback(context.Context, int) (io.WriteCloser, error)
	Describe() string
}

// PlaybackDrainer is optionally implemented by a backend whose Close method
// discards queued audio. Drain waits until already-written audio has played.
type PlaybackDrainer interface{ Drain() error }

// AudioFailureReporter optionally exposes the last underlying device error.
type AudioFailureReporter interface{ LastError() string }

// AudioDevice is a system supplied audio endpoint. Name is the complete name
// accepted by the backend; Description is its human readable label.
type AudioDevice struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Default     bool   `json:"default,omitempty"`
}

// AudioDeviceProvider optionally supports runtime endpoint discovery and
// selection. Device names are opaque to the SDK and are passed back unchanged.
type AudioDeviceProvider interface {
	AudioDevices() (inputs, outputs []AudioDevice, err error)
	SelectedAudioDevices() (input, output string)
	SelectAudioDevices(input, output string) error
}

// ClockedAudioOutput exposes the next hardware playback position for a
// synchronized stream such as Sendspin.
type ClockedAudioOutput interface {
	NextPlaybackTime() (time.Time, time.Duration, error)
	Write([]byte) (int, error)
	Close() error
}

// ClockedAudioBackend optionally provides hardware-clocked output.
type ClockedAudioBackend interface {
	OpenClockedOutput(context.Context, int, int) (ClockedAudioOutput, error)
}
