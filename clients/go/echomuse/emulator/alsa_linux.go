//go:build linux && cgo

package emulator

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
    int (*delay_fn)(void *, long *);
    int (*recover_fn)(void *, int, int);
    int (*drop_fn)(void *);
    int (*close_fn)(void *);
} em_alsa;

static int em_alsa_open(em_alsa *a, const char *device, unsigned int rate,
                        unsigned int channels, unsigned int latency_us) {
    a->lib = dlopen("libasound.so.2", RTLD_NOW | RTLD_LOCAL);
    if (!a->lib) return -ENOSYS;
#define LOAD(name, field) do { *(void **)(&a->field) = dlsym(a->lib, name); if (!a->field) return -ENOSYS; } while (0)
    LOAD("snd_pcm_open", open_fn);
    LOAD("snd_pcm_set_params", set_params_fn);
    LOAD("snd_pcm_writei", writei_fn);
    LOAD("snd_pcm_delay", delay_fn);
    LOAD("snd_pcm_recover", recover_fn);
    LOAD("snd_pcm_drop", drop_fn);
    LOAD("snd_pcm_close", close_fn);
#undef LOAD
    // ALSA constants: playback=0, S16_LE=2, RW_INTERLEAVED=3.
    int rc = a->open_fn(&a->pcm, device, 0, 0);
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

static int em_alsa_recover(em_alsa *a, int err) {
    return a->recover_fn(a->pcm, err, 1);
}

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
	"time"
	"unsafe"
)

type alsaSendspinBackend struct{}

func defaultSendspinOutputBackend() SendspinOutputBackend { return alsaSendspinBackend{} }

func (alsaSendspinBackend) OpenSendspinOutput(ctx context.Context, rate, channels int) (ClockedSendspinOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	device := C.CString("default")
	defer C.free(unsafe.Pointer(device))
	var handle C.em_alsa
	if rc := C.em_alsa_open(&handle, device, C.uint(rate), C.uint(channels), 100000); rc < 0 {
		return nil, fmt.Errorf("open ALSA Sendspin output: %w", alsaError(int(rc)))
	}
	return &alsaSendspinOutput{ctx: ctx, handle: handle, rate: rate, channels: channels}, nil
}

type alsaSendspinOutput struct {
	ctx      context.Context
	handle   C.em_alsa
	rate     int
	channels int
	closed   bool
}

func (o *alsaSendspinOutput) NextPlaybackTime() (time.Time, time.Duration, error) {
	if err := o.ctx.Err(); err != nil {
		return time.Time{}, 0, err
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

func (o *alsaSendspinOutput) Write(p []byte) (int, error) {
	frameBytes := o.channels * 2
	if len(p)%frameBytes != 0 {
		return 0, fmt.Errorf("ALSA Sendspin output: %d bytes is not a whole audio frame", len(p))
	}
	written := 0
	for written < len(p) {
		if err := o.ctx.Err(); err != nil {
			return written, err
		}
		ptr := unsafe.Pointer(&p[written])
		frames := C.em_alsa_write(&o.handle, ptr, C.ulong((len(p)-written)/frameBytes))
		if frames < 0 {
			rc := C.em_alsa_recover(&o.handle, C.int(frames))
			if rc < 0 {
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

func (o *alsaSendspinOutput) Close() error {
	if !o.closed {
		C.em_alsa_close(&o.handle)
		o.closed = true
	}
	return nil
}

func alsaError(code int) error {
	return errors.New(C.GoString(C.strerror(C.int(-code))))
}
