package wakeword

import (
	"errors"
	"sync"
	"time"
)

// Listener connects a Detector to trigger policy. It has no OS or inference-runtime dependency:
// an application supplies a local Inferer (for example, one backed by its installed ONNX Runtime).
// Keep one Listener per microphone and drive Push from one goroutine.
type Listener struct {
	mu         sync.Mutex
	Detector   *Detector
	Trigger    *Trigger
	Shadow     bool
	OnCrossing func(Crossing, bool)
}

// NewListener builds a local wake listener around an application-supplied inference engine.
func NewListener(inf Inferer, threshold float32, shadow bool, onCrossing func(Crossing, bool)) (*Listener, error) {
	if threshold <= 0 || threshold > 1 {
		return nil, errors.New("echomuse/wakeword: threshold must be greater than 0 and at most 1")
	}
	detector, err := New(inf)
	if err != nil {
		return nil, err
	}
	l := &Listener{Detector: detector, Shadow: shadow, OnCrossing: onCrossing}
	l.Trigger = NewTrigger(threshold, func(c Crossing) bool {
		if l.OnCrossing != nil {
			l.OnCrossing(c, !l.Shadow)
		}
		return !l.Shadow
	})
	return l, nil
}

// Push scores the audio. It returns ErrNotReady during warmup or when no full audio chunk arrived.
// A crossing callback's second argument is true only when the application should open a voice turn.
func (l *Listener) Push(pcm []byte) (float32, error) {
	if l == nil || l.Detector == nil || l.Trigger == nil {
		return 0, errors.New("echomuse/wakeword: incomplete listener")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(pcm)%2 != 0 {
		return 0, errors.New("echomuse/wakeword: input must contain whole 16-bit samples")
	}
	var latest float32
	ready := false
	for len(pcm) > 0 {
		n := ChunkSamples * 2
		if len(pcm) < n {
			n = len(pcm)
		}
		score, err := l.Detector.Push(pcm[:n])
		pcm = pcm[n:]
		if err == ErrNotReady {
			continue
		}
		if err != nil {
			return 0, err
		}
		latest, ready = score, true
		_, _ = l.Trigger.Consider(score, time.Now())
	}
	if !ready {
		return 0, ErrNotReady
	}
	return latest, nil
}

// Reset clears detector history and refractory timing after a capture restart.
func (l *Listener) Reset() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.Detector != nil {
		l.Detector.Reset()
	}
	if l.Trigger != nil {
		l.Trigger.last = time.Time{}
	}
}

// Configure changes the score threshold and shadow policy between frames.
func (l *Listener) Configure(threshold float32, shadow bool) {
	if l == nil || l.Trigger == nil {
		return
	}
	l.mu.Lock()
	if threshold > 0 && threshold <= 1 {
		l.Trigger.Threshold = threshold
	}
	l.Shadow = shadow
	l.mu.Unlock()
}
