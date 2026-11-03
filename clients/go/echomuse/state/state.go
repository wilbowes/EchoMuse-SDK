// Package state is what an application reads to know what the device is
// doing.
//
// The controller drives the ring through frames that describe colours and
// animations, and a device with a screen has a different vocabulary: it
// wants to know whether it is listening, thinking, speaking or muted. This
// package derives that state from the messages that actually arrive, so an
// application never has to interpret the protocol itself.
//
// Every state is reported with the reason it is in that state, and every
// state that is not yet known is reported as unknown rather than as its
// opposite. A device that has just connected is not muted, and saying so
// would be a lie the application might act on.
package state

import (
	"sync"
	"time"

	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/protocol"
)

// Phase is the coarse state of the voice assistant.
type Phase string

const (
	// PhaseUnknown — nothing has been said yet, or the link dropped and
	// the last known state cannot be trusted.
	PhaseUnknown Phase = "unknown"
	// PhaseIdle — connected, not doing anything.
	PhaseIdle Phase = "idle"
	// PhaseListening — the microphone is open and the user may be speaking.
	PhaseListening Phase = "listening"
	// PhaseThinking — the wake word fired and the controller is waiting
	// for Home Assistant.
	PhaseThinking Phase = "thinking"
	// PhaseSpeaking — the response is playing.
	PhaseSpeaking Phase = "speaking"
	// PhaseMuted — the device's own mute switch is on.
	PhaseMuted Phase = "muted"
	// PhasePending — the controller has not admitted the device yet.
	PhasePending Phase = "pending"
	// PhaseRefused — the controller refused the device's credentials.
	PhaseRefused Phase = "refused"
	// PhaseDisconnected — the link is down.
	PhaseDisconnected Phase = "disconnected"
	// PhaseReady — the controller admitted the device and the link is up.
	PhaseReady Phase = "ready"
)

// ListenMode is what the device is doing with its microphone.
type ListenMode string

const (
	// ListenUnknown — the device has not said.
	ListenUnknown ListenMode = "unknown"
	// ListenLocal — the device scores its own wake word and sends nothing
	// until it fires. No audio leaves the device until then.
	ListenLocal ListenMode = "local"
	// ListenStream — the device streams continuously to the controller.
	ListenStream ListenMode = "stream"
	// ListenDegraded — the device cannot run its own wake word and is
	// button-only.
	ListenDegraded ListenMode = "degraded"
)

// Streams reports whether audio is leaving the device continuously.
//
// It is true only for the streaming mode, and false for the local mode —
// where the device sends nothing until its wake word fires. It is the one
// question a privacy statement has to answer, so it is answered from what
// the device reported rather than from what it was configured to do.
func (m ListenMode) Streams() bool { return m == ListenStream }

// Snapshot is the whole observable state of the device at one instant.
//
// It is a value, not a live view: reading it never blocks and never races
// with the link.
type Snapshot struct {
	// Phase is the coarse state.
	Phase Phase
	// PhaseReason says why the phase is what it is, in words an
	// application can show a person.
	PhaseReason string

	// Listen is what the device is doing with its microphone.
	Listen ListenMode
	// ListenReason explains a degraded or unknown listen mode.
	ListenReason string

	// Muted is the device's own mute switch.
	Muted bool

	// Speaking is true while a response is playing.
	Speaking bool
	// Thinking is true while the controller waits for Home Assistant.
	Thinking bool
	// Listening is true while the microphone is open.
	Listening bool

	// Volume is the last volume level the device reported.
	Volume int

	// LinkUp is whether the control plane is connected.
	LinkUp bool
	// Approved is whether the controller has admitted the device.
	Approved bool
	// Features are the capabilities the controller announced on its ack.
	Features []string

	// Capabilities are the capabilities this device announced.
	Capabilities []protocol.Capability

	// LastError is the most recent error, if any.
	LastError string

	// UpdatedAt is when this snapshot was taken.
	UpdatedAt time.Time
}

// Tracker holds the device state and tells subscribers when it changes.
//
// The SDK owns the tracker; the application reads snapshots and subscribes
// to changes. Nothing in the application has to parse a protocol message to
// know what the device is doing.
type Tracker struct {
	mu        sync.RWMutex
	current   Snapshot
	listeners []func(Snapshot)
}

// NewTracker returns a tracker in the unknown state.
func NewTracker() *Tracker {
	return &Tracker{
		current: Snapshot{
			Phase:     PhaseUnknown,
			Listen:    ListenUnknown,
			UpdatedAt: time.Now(),
		},
	}
}

// Snapshot returns the current state.
func (t *Tracker) Snapshot() Snapshot {
	t.mu.RLock()
	defer t.mu.RUnlock()
	s := t.current
	s.Capabilities = append([]protocol.Capability(nil), t.current.Capabilities...)
	s.Features = append([]string(nil), t.current.Features...)
	return s
}

// OnChange registers a listener. It is called with every change, on the
// goroutine that made the change, so a listener that blocks blocks the
// link — hand work to another goroutine rather than doing it here.
func (t *Tracker) OnChange(fn func(Snapshot)) {
	t.mu.Lock()
	t.listeners = append(t.listeners, fn)
	t.mu.Unlock()
}

// update applies a change and notifies the listeners if anything moved.
func (t *Tracker) update(mutate func(*Snapshot)) {
	t.mu.Lock()
	before := t.current
	mutate(&t.current)
	t.current.UpdatedAt = time.Now()
	after := t.current
	listeners := append([]func(Snapshot){}, t.listeners...)
	t.mu.Unlock()

	if equal(before, after) {
		return
	}
	for _, fn := range listeners {
		fn(after)
	}
}

// equal compares two snapshots.
//
// The slices are compared element by element, because a struct holding a
// slice is not comparable with == — and a comparison that does not compile
// is a comparison that never gets written.
func equal(a, b Snapshot) bool {
	if a.Phase != b.Phase || a.PhaseReason != b.PhaseReason {
		return false
	}
	if a.Listen != b.Listen || a.ListenReason != b.ListenReason {
		return false
	}
	if a.Muted != b.Muted || a.Speaking != b.Speaking ||
		a.Thinking != b.Thinking || a.Listening != b.Listening {
		return false
	}
	if a.Volume != b.Volume || a.LinkUp != b.LinkUp ||
		a.Approved != b.Approved || a.LastError != b.LastError {
		return false
	}
	if len(a.Features) != len(b.Features) {
		return false
	}
	for i := range a.Features {
		if a.Features[i] != b.Features[i] {
			return false
		}
	}
	if len(a.Capabilities) != len(b.Capabilities) {
		return false
	}
	for i := range a.Capabilities {
		if a.Capabilities[i] != b.Capabilities[i] {
			return false
		}
	}
	return true
}

// SetPhase records the coarse state and its reason.
func (t *Tracker) SetPhase(phase Phase, reason string) {
	t.update(func(s *Snapshot) {
		s.Phase = phase
		s.PhaseReason = reason
	})
}

// SetListen records what the device is doing with its microphone.
func (t *Tracker) SetListen(mode ListenMode, reason string) {
	t.update(func(s *Snapshot) {
		s.Listen = mode
		s.ListenReason = reason
	})
}

// SetMuted records the device's mute switch.
func (t *Tracker) SetMuted(muted bool) {
	t.update(func(s *Snapshot) {
		s.Muted = muted
		if muted {
			s.Phase = PhaseMuted
			s.PhaseReason = "the device's mute switch is on"
		} else if s.Phase == PhaseMuted {
			s.Phase = PhaseIdle
			s.PhaseReason = ""
		}
	})
}

// SetSpeaking records that a response is playing.
func (t *Tracker) SetSpeaking(speaking bool) {
	t.update(func(s *Snapshot) {
		s.Speaking = speaking
	})
}

// SetThinking records that the controller is waiting for Home Assistant.
func (t *Tracker) SetThinking(thinking bool) {
	t.update(func(s *Snapshot) {
		s.Thinking = thinking
	})
}

// SetListening records that the microphone is open.
func (t *Tracker) SetListening(listening bool) {
	t.update(func(s *Snapshot) {
		s.Listening = listening
	})
}

// SetVolume records the last reported volume level.
func (t *Tracker) SetVolume(level int) {
	t.update(func(s *Snapshot) {
		s.Volume = level
	})
}

// SetLink records whether the control plane is connected.
func (t *Tracker) SetLink(up bool) {
	t.update(func(s *Snapshot) {
		s.LinkUp = up
		if up {
			return
		}
		// A device waiting for approval is disconnected from the
		// controller by definition — the controller closes the
		// connection and the device redials. That is not a fault, so
		// the phase stays pending rather than becoming a fault.
		switch s.Phase {
		case PhaseRefused, PhasePending, PhaseReady:
			return
		}
		s.Phase = PhaseDisconnected
		s.PhaseReason = "the link to the controller is down"
	})
}

// SetApproved records whether the controller has admitted the device.
func (t *Tracker) SetApproved(approved bool) {
	t.update(func(s *Snapshot) {
		s.Approved = approved
		if approved && s.LinkUp {
			s.Phase = PhaseReady
			s.PhaseReason = "the controller admitted this device"
		}
	})
}

// SetFeatures records the capabilities the controller announced.
func (t *Tracker) SetFeatures(features []string) {
	t.update(func(s *Snapshot) {
		s.Features = append([]string(nil), features...)
	})
}

// SetCapabilities records the capabilities this device announced.
func (t *Tracker) SetCapabilities(caps []protocol.Capability) {
	t.update(func(s *Snapshot) {
		s.Capabilities = append([]protocol.Capability(nil), caps...)
	})
}

// SetError records the most recent error.
func (t *Tracker) SetError(err error) {
	t.update(func(s *Snapshot) {
		if err == nil {
			s.LastError = ""
			return
		}
		s.LastError = err.Error()
	})
}
