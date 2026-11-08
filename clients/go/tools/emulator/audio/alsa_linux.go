//go:build linux && cgo

package audio

/*
#cgo LDFLAGS: -ldl
#include <dlfcn.h>
#include <errno.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

typedef struct {
    void *lib;
    void *pcm;
    int (*open_fn)(void **, const char *, int, int);
    int (*set_params_fn)(void *, int, int, unsigned int, unsigned int, int, unsigned int);
    long (*writei_fn)(void *, const void *, unsigned long);
    long (*readi_fn)(void *, void *, unsigned long);
    int (*delay_fn)(void *, long *);
    int (*recover_fn)(void *, int, int);
    int (*drain_fn)(void *);
    int (*drop_fn)(void *);
    int (*close_fn)(void *);
} em_alsa;

static int em_alsa_open(em_alsa *a, const char *device, int stream, unsigned int rate,
                        unsigned int channels, unsigned int latency_us) {
    a->lib = dlopen("libasound.so.2", RTLD_NOW | RTLD_LOCAL);
    if (!a->lib) return -ENOSYS;
#define LOAD(name, field) do { *(void **)(&a->field) = dlsym(a->lib, name); if (!a->field) return -ENOSYS; } while (0)
    LOAD("snd_pcm_open", open_fn);
    LOAD("snd_pcm_set_params", set_params_fn);
    LOAD("snd_pcm_writei", writei_fn);
    LOAD("snd_pcm_readi", readi_fn);
    LOAD("snd_pcm_delay", delay_fn);
    LOAD("snd_pcm_recover", recover_fn);
    LOAD("snd_pcm_drain", drain_fn);
    LOAD("snd_pcm_drop", drop_fn);
    LOAD("snd_pcm_close", close_fn);
#undef LOAD
    // ALSA constants: playback=0, S16_LE=2, RW_INTERLEAVED=3.
    int rc = a->open_fn(&a->pcm, device, stream, 0);
    if (rc < 0) return rc;
    rc = a->set_params_fn(a->pcm, 2, 3, channels, rate, 1, latency_us);
    if (rc < 0) {
        a->close_fn(a->pcm);
        a->pcm = NULL;
        return rc;
    }
    return 0;
}

static long em_alsa_delay(em_alsa *a, int *err) {
    long frames = 0;
    *err = a->delay_fn(a->pcm, &frames);
    return frames;
}

static long em_alsa_write(em_alsa *a, const void *data, unsigned long frames) {
    return a->writei_fn(a->pcm, data, frames);
}
static long em_alsa_read(em_alsa *a, void *data, unsigned long frames) {
    return a->readi_fn(a->pcm, data, frames);
}

static int em_alsa_recover(em_alsa *a, int err) {
    return a->recover_fn(a->pcm, err, 1);
}
static int em_alsa_drain(em_alsa *a) { return a->drain_fn(a->pcm); }

static void em_alsa_close(em_alsa *a) {
    if (a->pcm) {
        a->drop_fn(a->pcm);
        a->close_fn(a->pcm);
        a->pcm = NULL;
    }
    if (a->lib) {
        dlclose(a->lib);
        a->lib = NULL;
    }
}
*/
import "C"

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse"
	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/emulator"
)

// alsaBackend uses ALSA directly for both directions of voice audio.
var _ echomuse.AudioBackend = (*alsaBackend)(nil)
var _ echomuse.AudioDeviceProvider = (*alsaBackend)(nil)
var _ echomuse.ClockedAudioBackend = (*alsaBackend)(nil)

type alsaBackend struct {
	mu            sync.RWMutex
	input, output string
	lastError     string
}

// NewBackend creates the command's direct ALSA backend. Empty names use
// ALSA's default PCM. Device names may be full ALSA PCM names such as
// "plughw:CARD=USB,DEV=0".
func NewBackend(input, output string) emulator.AudioBackend {
	return &alsaBackend{input: alsaTarget(input), output: alsaTarget(output)}
}

func alsaTarget(name string) string {
	name = strings.TrimSpace(name)
	name = strings.TrimPrefix(name, "alsa:")
	if name == "" {
		return "default"
	}
	if strings.HasPrefix(name, "hw:") {
		return "plughw:" + strings.TrimPrefix(name, "hw:")
	}
	return name
}

func (b *alsaBackend) Describe() string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return fmt.Sprintf("ALSA (%s → %s)", b.input, b.output)
}
func (b *alsaBackend) SelectedAudioDevices() (string, string) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	name := func(raw string) string {
		if raw == "default" {
			return raw
		}
		return FormatALSA + ":" + raw
	}
	return name(b.input), name(b.output)
}
func (b *alsaBackend) LastError() string  { b.mu.RLock(); defer b.mu.RUnlock(); return b.lastError }
func (b *alsaBackend) setError(err error) { b.mu.Lock(); b.lastError = err.Error(); b.mu.Unlock() }
func (b *alsaBackend) SelectAudioDevices(input, output string) error {
	inputs, outputs, err := ListDevices()
	if err != nil {
		return err
	}
	valid := func(ds []emulator.AudioDevice, name string) bool {
		for _, d := range ds {
			if d.Name == name {
				return true
			}
		}
		return false
	}
	if !valid(inputs, input) || !valid(outputs, output) {
		return fmt.Errorf("unknown ALSA audio device")
	}
	b.mu.Lock()
	b.input = alsaTarget(input)
	b.output = alsaTarget(output)
	b.mu.Unlock()
	return nil
}
func (b *alsaBackend) AudioDevices() ([]emulator.AudioDevice, []emulator.AudioDevice, error) {
	return ListDevices()
}

func openALSA(device string, stream, rate, channels int) (C.em_alsa, error) {
	name := C.CString(device)
	defer C.free(unsafe.Pointer(name))
	var h C.em_alsa
	if rc := C.em_alsa_open(&h, name, C.int(stream), C.uint(rate), C.uint(channels), 100000); rc < 0 {
		return h, fmt.Errorf("open ALSA device %q: %w", device, alsaError(int(rc)))
	}
	return h, nil
}

func (b *alsaBackend) Capture(ctx context.Context, rate int) (io.ReadCloser, error) {
	b.mu.RLock()
	device := b.input
	b.mu.RUnlock()
	h, err := openALSA(device, 1, rate, 1)
	if err != nil {
		b.setError(err)
		return nil, err
	}
	return &alsaCapture{ctx: ctx, handle: h}, nil
}
func (b *alsaBackend) Playback(ctx context.Context, rate int) (io.WriteCloser, error) {
	b.mu.RLock()
	device := b.output
	b.mu.RUnlock()
	h, err := openALSA(device, 0, rate, 1)
	if err != nil {
		b.setError(err)
		return nil, err
	}
	return &alsaPlayback{ctx: ctx, handle: h}, nil
}

func (b *alsaBackend) OpenClockedOutput(ctx context.Context, rate, channels int) (echomuse.ClockedAudioOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b.mu.RLock()
	device := b.output
	b.mu.RUnlock()
	h, err := openALSA(device, 0, rate, channels)
	if err != nil {
		b.setError(err)
		return nil, err
	}
	return &alsaClockedOutput{ctx: ctx, handle: h, rate: rate, channels: channels}, nil
}

type alsaClockedOutput struct {
	ctx            context.Context
	handle         C.em_alsa
	rate, channels int
	mu             sync.Mutex
	closed         bool
}

func (o *alsaClockedOutput) NextPlaybackTime() (time.Time, time.Duration, error) {
	if err := o.ctx.Err(); err != nil {
		return time.Time{}, 0, err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return time.Time{}, 0, io.ErrClosedPipe
	}
	var code C.int
	frames := C.em_alsa_delay(&o.handle, &code)
	if code < 0 {
		return time.Time{}, 0, alsaError(int(code))
	}
	if frames < 0 {
		frames = 0
	}
	return time.Now().Add(time.Duration(frames) * time.Second / time.Duration(o.rate)), 250 * time.Microsecond, nil
}

func (o *alsaClockedOutput) Write(p []byte) (int, error) {
	frameBytes := o.channels * 2
	if len(p)%frameBytes != 0 {
		return 0, fmt.Errorf("ALSA clocked output: %d bytes is not a whole audio frame", len(p))
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return 0, io.ErrClosedPipe
	}
	written := 0
	for written < len(p) {
		if err := o.ctx.Err(); err != nil {
			return written, err
		}
		frames := C.em_alsa_write(&o.handle, unsafe.Pointer(&p[written]), C.ulong((len(p)-written)/frameBytes))
		if frames < 0 {
			if rc := C.em_alsa_recover(&o.handle, C.int(frames)); rc < 0 {
				return written, alsaError(int(rc))
			}
			continue
		}
		if frames == 0 {
			continue
		}
		written += int(frames) * frameBytes
	}
	return written, nil
}

func (o *alsaClockedOutput) Close() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.closed {
		C.em_alsa_close(&o.handle)
		o.closed = true
	}
	return nil
}

type alsaCapture struct {
	ctx    context.Context
	handle C.em_alsa
	mu     sync.Mutex
	closed bool
}

func (r *alsaCapture) Read(p []byte) (int, error) {
	if len(p) < 2 {
		return 0, nil
	}
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return 0, io.EOF
	}
	frames := C.em_alsa_read(&r.handle, unsafe.Pointer(&p[0]), C.ulong(len(p)/2))
	if frames < 0 {
		if rc := C.em_alsa_recover(&r.handle, C.int(frames)); rc < 0 {
			return 0, alsaError(int(rc))
		}
		return 0, nil
	}
	return int(frames) * 2, nil
}
func (r *alsaCapture) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.closed {
		C.em_alsa_close(&r.handle)
		r.closed = true
	}
	return nil
}

type alsaPlayback struct {
	ctx    context.Context
	handle C.em_alsa
	mu     sync.Mutex
	closed bool
}

func (w *alsaPlayback) Write(p []byte) (int, error) {
	if len(p)%2 != 0 {
		return 0, fmt.Errorf("ALSA playback expects 16-bit PCM")
	}
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return 0, io.ErrClosedPipe
	}
	done := 0
	for done < len(p) {
		n := C.em_alsa_write(&w.handle, unsafe.Pointer(&p[done]), C.ulong((len(p)-done)/2))
		if n < 0 {
			if rc := C.em_alsa_recover(&w.handle, C.int(n)); rc < 0 {
				return done, alsaError(int(rc))
			}
			continue
		}
		if n == 0 {
			continue
		}
		done += int(n) * 2
	}
	return done, nil
}
func (w *alsaPlayback) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.closed {
		C.em_alsa_close(&w.handle)
		w.closed = true
	}
	return nil
}

// Drain waits until ALSA has played every frame queued before the response EOS.
func (w *alsaPlayback) Drain() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	if rc := C.em_alsa_drain(&w.handle); rc < 0 {
		return alsaError(int(rc))
	}
	return nil
}

func alsaError(code int) error {
	return errors.New(C.GoString(C.strerror(C.int(-code))))
}
