// Package display turns the controller's ring frames into something a
// screen can show.
//
// The controller's status surface is a ring of twelve indicators. It
// renders every frame of every animation and sends the result, so a device
// with a ring paints exactly what an Echo paints. A device with a screen
// has a different vocabulary: it wants a colour, a shape and a line of
// text.
//
// This package is the bridge. It reads the ring frames and works out what
// they mean — a colour, an animation pattern, whether this is the
// listening ring — so an application can draw it without knowing the
// ring's vocabulary.
//
// It is deliberately not a rendering library. It produces values, and the
// application draws them.
package display

import (
	"sync"
	"time"

	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/protocol"
)

// Colour is an RGB colour.
type Colour struct {
	R uint8
	G uint8
	B uint8
}

// Pattern is what an animation does.
type Pattern string

const (
	// PatternNone — no animation is in force.
	PatternNone Pattern = ""
	// PatternOff — the ring is cleared.
	PatternOff Pattern = "off"
	// PatternSolid — a fixed colour.
	PatternSolid Pattern = "solid"
	// PatternSpin — a head and a trail moving around.
	PatternSpin Pattern = "spin"
	// PatternRotate — a palette rotating.
	PatternRotate Pattern = "rotate"
	// PatternPulse — a throb.
	PatternPulse Pattern = "pulse"
	// PatternMeter — brightness following the playback level.
	PatternMeter Pattern = "meter"
)

// View is what the display should show.
type View struct {
	// Pattern is the animation in force.
	Pattern Pattern
	// Colour is the dominant colour of the ring.
	Colour Colour
	// Secondary is the second colour, for the patterns that have one:
	// the trail of a spin, or a palette that is rotated.
	Secondary Colour
	// Palette is every colour of the ring, in order, for a display that
	// can show more than one.
	Palette []Colour
	// Listening marks the listening ring. It is the one thing on the ring
	// that means something the colours do not: the controller marks the
	// listening frame so the beamformer's direction overlay can key off
	// it.
	Listening bool
	// PeriodMs is the animation's frame interval.
	PeriodMs int
	// ExpiresAt is when the animation's dead-man switch fires, and the
	// display should go back to idle. It is zero when the animation has
	// no dead-man.
	ExpiresAt time.Time
	// UpdatedAt is when this view was produced.
	UpdatedAt time.Time
}

// Idle reports whether nothing is being shown.
func (v View) Idle() bool { return v.Pattern == PatternNone || v.Pattern == PatternOff }

// Tracker follows the ring frames and produces views.
//
// It is safe to use from any goroutine.
type Tracker struct {
	mu   sync.RWMutex
	view View
}

// NewTracker returns a tracker in the idle state.
func NewTracker() *Tracker {
	return &Tracker{
		view: View{Pattern: PatternNone, UpdatedAt: time.Now()},
	}
}

// View returns the current view.
func (t *Tracker) View() View {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.view
}

// OnLEDs records an LED frame.
//
// A frame with no indicators is the ring being cleared.
func (t *Tracker) OnLEDs(frame protocol.LEDs) {
	t.update(func(v *View) {
		v.Pattern = PatternSolid
		v.Palette = nil
		for _, led := range frame.LEDs {
			v.Palette = append(v.Palette, Colour{R: uint8(led.R), G: uint8(led.G), B: uint8(led.B)})
		}
		if len(v.Palette) > 0 {
			v.Colour = dominant(v.Palette)
		}
		if len(v.Palette) > 1 {
			v.Secondary = v.Palette[1]
		}
		if frame.Listening != nil {
			v.Listening = *frame.Listening
		}
		v.ExpiresAt = time.Time{}
	})
}

// OnLEDAnim records an animation spec.
//
// The spec's dead-man switch matters to a display: when it fires the
// controller is gone, and the display has to stop showing an animation
// that no longer means anything.
func (t *Tracker) OnLEDAnim(spec protocol.AnimSpec) {
	t.update(func(v *View) {
		v.Pattern = Pattern(spec.Pattern)
		v.Palette = nil
		for _, c := range spec.Colors {
			v.Palette = append(v.Palette, Colour{R: c[0], G: c[1], B: c[2]})
		}
		if len(v.Palette) > 0 {
			v.Colour = v.Palette[0]
		}
		if len(v.Palette) > 1 {
			v.Secondary = v.Palette[1]
		}
		v.Listening = spec.Listening
		v.PeriodMs = spec.PeriodMs
		if spec.TTLSec > 0 {
			v.ExpiresAt = time.Now().Add(time.Duration(spec.TTLSec) * time.Second)
		} else {
			v.ExpiresAt = time.Time{}
		}
	})
}

// Reset clears the view, which is what a device does when its link drops.
//
// The ring goes dark on a dropped link rather than freezing on the last
// thing it showed: a frozen display says something is happening, and
// nothing is.
func (t *Tracker) Reset() {
	t.update(func(v *View) {
		v.Pattern = PatternNone
		v.Palette = nil
		v.Colour = Colour{}
		v.Secondary = Colour{}
		v.Listening = false
		v.ExpiresAt = time.Time{}
	})
}

// update applies a change.
func (t *Tracker) update(mutate func(*View)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	mutate(&t.view)
	t.view.UpdatedAt = time.Now()
}

// dominant picks the brightest colour of a palette.
//
// The brightest is the one that carries the meaning: on the listening ring
// it is the green, and on a muted device it is the red.
func dominant(palette []Colour) Colour {
	best := palette[0]
	bestLuma := luma(best)
	for _, c := range palette[1:] {
		if l := luma(c); l > bestLuma {
			best, bestLuma = c, l
		}
	}
	return best
}

// luma is a colour's perceived brightness.
func luma(c Colour) int {
	return int(c.R)*299 + int(c.G)*587 + int(c.B)*114
}
