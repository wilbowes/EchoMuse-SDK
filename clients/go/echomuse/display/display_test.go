package display_test

import (
	"testing"
	"time"

	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/display"
	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/protocol"
)

// TestTrackerReadsAnLEDFrame checks that a ring frame becomes a view.
func TestTrackerReadsAnLEDFrame(t *testing.T) {
	tracker := display.NewTracker()

	tracker.OnLEDs(protocol.LEDs{
		Type: protocol.TypeLEDs,
		LEDs: []protocol.LED{
			{ID: 0, R: 0, G: 255, B: 0},
			{ID: 1, R: 0, G: 0, B: 0},
		},
	})

	view := tracker.View()
	if view.Pattern != display.PatternSolid {
		t.Errorf("the pattern was %q, want %q", view.Pattern, display.PatternSolid)
	}
	if view.Colour.G != 255 {
		t.Errorf("the dominant colour was %+v, want the green one", view.Colour)
	}
	if len(view.Palette) != 2 {
		t.Errorf("the palette held %d colours, want 2", len(view.Palette))
	}
}

// TestTrackerReadsTheListeningFlag checks the one thing on the ring that
// means something the colours do not.
func TestTrackerReadsTheListeningFlag(t *testing.T) {
	tracker := display.NewTracker()
	listening := true

	tracker.OnLEDs(protocol.LEDs{
		Type:      protocol.TypeLEDs,
		LEDs:      []protocol.LED{{ID: 0, R: 0, G: 255, B: 0}},
		Listening: &listening,
	})

	if !tracker.View().Listening {
		t.Error("the listening ring was not marked as listening")
	}
}

// TestTrackerReadsAnAnimation checks that an animation spec becomes a view,
// and that its dead-man switch is carried.
func TestTrackerReadsAnAnimation(t *testing.T) {
	tracker := display.NewTracker()

	tracker.OnLEDAnim(protocol.AnimSpec{
		Pattern:  "spin",
		Colors:   [][3]uint8{{0, 255, 0}, {0, 128, 0}},
		PeriodMs: 80,
		TTLSec:   30,
	})

	view := tracker.View()
	if view.Pattern != display.PatternSpin {
		t.Errorf("the pattern was %q, want %q", view.Pattern, display.PatternSpin)
	}
	if view.PeriodMs != 80 {
		t.Errorf("the period was %d, want 80", view.PeriodMs)
	}
	if view.ExpiresAt.IsZero() {
		t.Error("the animation's dead-man switch was not carried")
	}
	if time.Until(view.ExpiresAt) > 31*time.Second {
		t.Error("the dead-man switch expires too far away")
	}
}

// TestTrackerResetClearsTheView checks that a dropped link clears the
// display.
//
// A frozen display says something is happening, and nothing is.
func TestTrackerResetClearsTheView(t *testing.T) {
	tracker := display.NewTracker()
	tracker.OnLEDAnim(protocol.AnimSpec{
		Pattern: "pulse",
		Colors:  [][3]uint8{{255, 0, 0}},
		TTLSec:  30,
	})

	tracker.Reset()

	view := tracker.View()
	if !view.Idle() {
		t.Errorf("the view after a reset was %q, want idle", view.Pattern)
	}
	if view.Colour != (display.Colour{}) {
		t.Error("the colour survived the reset")
	}
}

// TestTrackerPicksTheBrightestColour checks that the dominant colour is
// the brightest one, which is the one that carries the meaning.
func TestTrackerPicksTheBrightestColour(t *testing.T) {
	tracker := display.NewTracker()

	tracker.OnLEDs(protocol.LEDs{
		LEDs: []protocol.LED{
			{ID: 0, R: 10, G: 10, B: 10},
			{ID: 1, R: 255, G: 0, B: 0},
			{ID: 2, R: 0, G: 0, B: 5},
		},
	})

	if got := tracker.View().Colour; got.R != 255 {
		t.Errorf("the dominant colour was %+v, want the red one", got)
	}
}
