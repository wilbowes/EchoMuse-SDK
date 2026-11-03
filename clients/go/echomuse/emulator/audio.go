package emulator

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// playbackDrainTimeout is how long the speaker is given to play what it
// holds before it is stopped.
//
// A response is a few seconds of audio, and the player holds the tail of
// it until it is told the stream is over. The timeout is the backstop
// for a player that will not exit on its own.
const playbackDrainTimeout = 10 * time.Second

// AudioBackend is how the emulator reaches the machine's audio.
//
// It is an interface so the emulator can be driven on a machine with no
// sound card, and so a test can drive it with silence.
type AudioBackend interface {
	// Capture starts a microphone stream at the capture rate and returns
	// a reader of signed 16-bit little-endian mono samples.
	Capture(ctx context.Context, rate int) (io.ReadCloser, error)
	// Playback starts a speaker stream at the playback rate and returns a
	// writer of signed 16-bit little-endian mono samples.
	Playback(ctx context.Context, rate int) (io.WriteCloser, error)
	// Describe says what the backend is, for the window.
	Describe() string
}

// FailureReporter is a backend that can say why the audio failed.
//
// It is optional. A backend with nothing to add does not implement it,
// and the emulator reports the plain error instead.
type FailureReporter interface {
	// LastError says why the last capture or playback failed, or is
	// empty when it has no reason to give.
	LastError() string
}

// ffmpegBackend reaches the machine's audio through ffmpeg.
//
// It is the default because ffmpeg speaks every audio system — ALSA,
// PulseAudio and PipeWire on Linux, CoreAudio on macOS, DirectShow on
// Windows — through one command line, and it is nearly always already
// installed. It is not a dependency of the SDK: only the emulator uses
// it, and only when it is asked for.
//
// The default devices are the platform's own, the way a browser takes
// the system's default microphone and speaker. Nothing about the machine
// is assumed: a device is reached by the name ffmpeg gives it, and a
// machine with one sound card needs no name at all.
type ffmpegBackend struct {
	// input and output are the devices to use. An empty one means the
	// platform's default for that direction.
	input  Device
	output Device

	// lastError is why the last stream failed, read from ffmpeg's error
	// output.
	lastError string
	mu        sync.Mutex
}

// NewFFmpegBackend returns a backend that uses ffmpeg.
//
// An empty device uses the platform's default for each direction.
func NewFFmpegBackend(input, output string) AudioBackend {
	return &ffmpegBackend{
		input:  ParseDevice(input),
		output: ParseDevice(output),
	}
}

// NewFFmpegBackendForDevices returns a backend that uses ffmpeg on named
// devices.
func NewFFmpegBackendForDevices(input, output Device) AudioBackend {
	return &ffmpegBackend{input: input, output: output}
}

// Describe says what the backend is.
func (b *ffmpegBackend) Describe() string {
	if b.input.Name == DeviceDefault && b.output.Name == DeviceDefault {
		return "ffmpeg (the system default)"
	}
	return fmt.Sprintf("ffmpeg (%s → %s)", describeDevice(b.input), describeDevice(b.output))
}

// LastError says why the last stream failed.
//
// ffmpeg reports the reason a sound card refused the audio on its error
// output — "cannot set channel count to 1", "Device or resource busy" —
// and nowhere else. The write to its pipe fails first, and that failure
// says only that the pipe broke.
func (b *ffmpegBackend) LastError() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lastError
}

// setLastError records why a stream failed.
func (b *ffmpegBackend) setLastError(reason string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lastError = reason
}

// Capture starts a microphone stream.
//
// The stream is raw PCM, so ffmpeg is told not to wrap it in a container:
// the controller wants samples, not a file format.
func (b *ffmpegBackend) Capture(ctx context.Context, rate int) (io.ReadCloser, error) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return nil, fmt.Errorf("the emulator needs ffmpeg for the microphone: %w", err)
	}

	format, name := resolveDevice(b.input)

	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-hide_banner", "-loglevel", "error",
		"-f", format, "-i", name,
		"-ar", fmt.Sprint(rate),
		"-ac", "1",
		"-f", "s16le", "-",
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("opening the microphone's output: %w", err)
	}

	stderr := &stderrBuffer{}
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting the microphone: %w", err)
	}

	return &processReader{cmd: cmd, out: stdout, stderr: stderr, backend: b}, nil
}

// Playback starts a speaker stream.
//
// It goes through ffplay rather than ffmpeg. ffplay picks the platform's
// default output device for itself, and it takes the stream on its own
// terms: ffmpeg's output devices are per-platform and each refuses a
// format the protocol uses, while ffplay resamples and remixes whatever
// it is given.
func (b *ffmpegBackend) Playback(ctx context.Context, rate int) (io.WriteCloser, error) {
	if _, err := exec.LookPath("ffplay"); err != nil {
		return nil, fmt.Errorf("the emulator needs ffplay for the speaker: %w", err)
	}

	cmd := exec.CommandContext(ctx, "ffplay",
		"-nodisp",
		"-fflags", "nobuffer",
		"-autoexit",
		"-nostats",
		"-loglevel", "warning",
		"-f", "s16le",
		"-ar", fmt.Sprint(rate),
		"-i", "-",
	)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("opening the speaker's input: %w", err)
	}

	stderr := &stderrBuffer{}
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting the speaker: %w", err)
	}

	return &processWriter{cmd: cmd, in: stdin, stderr: stderr, backend: b}, nil
}

// processReader is the microphone's output, and the process behind it.
type processReader struct {
	cmd     *exec.Cmd
	out     io.ReadCloser
	stderr  *stderrBuffer
	backend *ffmpegBackend
	mu      sync.Mutex
}

// Read reads the captured samples.
//
// When the stream fails, the reason ffmpeg gave is reported instead of
// the pipe's own complaint: "cannot set channel count to 1" says what to
// fix, "broken pipe" does not.
//
// A device ffmpeg could not open at all ends the stream immediately, so
// the reason is read even at the end of it.
func (r *processReader) Read(p []byte) (int, error) {
	n, err := r.out.Read(p)
	if err != nil {
		if reason := r.stderr.Tail(); reason != "" {
			r.backend.setLastError(reason)
		}
	}
	return n, err
}

// Close stops the microphone.
func (r *processReader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.out.Close()
	if r.cmd.Process != nil {
		r.cmd.Process.Kill()
	}
	r.cmd.Wait()
	return nil
}

// processWriter is the speaker's input, and the process behind it.
type processWriter struct {
	cmd     *exec.Cmd
	in      io.WriteCloser
	stderr  *stderrBuffer
	backend *ffmpegBackend
	mu      sync.Mutex
}

// Write plays the samples.
//
// When the write fails, the reason ffplay gave is reported instead of the
// pipe's own complaint: "cannot set channel count to 1" says what to fix,
// "broken pipe" does not.
func (w *processWriter) Write(p []byte) (int, error) {
	n, err := w.in.Write(p)
	if err != nil {
		if reason := w.stderr.Tail(); reason != "" {
			w.backend.setLastError(reason)
			return n, fmt.Errorf("%s: %w", reason, err)
		}
	}
	return n, err
}

// Close stops the speaker.
//
// The audio is ended by closing the input, not by killing the process.
// The player buffers what it has been given, and a killed player takes
// the buffer with it: the response is lost silently, and the controller
// has already been told the response finished.
//
// Closing the input tells the player the stream is over, and the player
// plays what it holds before it exits.
func (w *processWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.in.Close()

	// The player drains and exits on its own. The wait is bounded so a
	// player that will not let go cannot hold the caller up.
	done := make(chan error, 1)
	go func() { done <- w.cmd.Wait() }()

	select {
	case <-done:
	case <-time.After(playbackDrainTimeout):
		if w.cmd.Process != nil {
			w.cmd.Process.Kill()
		}
		<-done
	}
	return nil
}

// orDefault returns the value, or the fallback when it is empty.
func orDefault(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

// ClockedSendspinOutput exposes the next hardware playback position for a
// Sendspin period. A real device clock is needed to align samples with other
// Sendspin players.
type ClockedSendspinOutput interface {
	NextPlaybackTime() (time.Time, time.Duration, error)
	Write([]byte) (int, error)
	Close() error
}

// SendspinOutputBackend optionally provides a hardware-clocked stereo output
// for Sendspin. The normal AudioBackend remains the fallback for voice audio.
type SendspinOutputBackend interface {
	OpenSendspinOutput(ctx context.Context, rate, channels int) (ClockedSendspinOutput, error)
}
