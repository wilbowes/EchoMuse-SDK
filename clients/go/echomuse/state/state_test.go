package state_test

import (
	"sync"
	"testing"

	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/state"
)

// TestTrackerStartsUnknown checks that a tracker reports what it does not
// know as unknown.
//
// A device that has just connected is not muted, and saying so would be a
// lie the application might act on.
func TestTrackerStartsUnknown(t *testing.T) {
	tracker := state.NewTracker()
	snap := tracker.Snapshot()

	if snap.Phase != state.PhaseUnknown {
		t.Errorf("the phase was %q, want %q", snap.Phase, state.PhaseUnknown)
	}
	if snap.Listen != state.ListenUnknown {
		t.Errorf("the listen mode was %q, want %q", snap.Listen, state.ListenUnknown)
	}
	if snap.Muted {
		t.Error("a tracker that knows nothing reported itself as muted")
	}
	if snap.LinkUp {
		t.Error("a tracker that knows nothing reported the link as up")
	}
}

// TestMuteSetsThePhase checks that the mute switch is device-sovereign and
// shows in the phase.
func TestMuteSetsThePhase(t *testing.T) {
	tracker := state.NewTracker()

	tracker.SetMuted(true)
	snap := tracker.Snapshot()
	if !snap.Muted {
		t.Error("the mute was not recorded")
	}
	if snap.Phase != state.PhaseMuted {
		t.Errorf("the phase was %q, want %q", snap.Phase, state.PhaseMuted)
	}
	if snap.PhaseReason == "" {
		t.Error("the muted phase carried no reason")
	}

	tracker.SetMuted(false)
	if tracker.Snapshot().Phase != state.PhaseIdle {
		t.Errorf("the phase after unmuting was %q, want %q",
			tracker.Snapshot().Phase, state.PhaseIdle)
	}
}

// TestListenModeAnswersThePrivacyQuestion checks the one question a
// privacy statement has to answer.
func TestListenModeAnswersThePrivacyQuestion(t *testing.T) {
	if state.ListenLocal.Streams() {
		t.Error("a device listening privately reported that it streams")
	}
	if !state.ListenStream.Streams() {
		t.Error("a streaming device reported that it does not stream")
	}
	if state.ListenDegraded.Streams() {
		t.Error("a degraded device reported that it streams")
	}
	if state.ListenUnknown.Streams() {
		t.Error("an unknown device reported that it streams")
	}
}

// TestOnChangeFiresOnAChange checks that the listeners fire when something
// moves, and not when nothing does.
func TestOnChangeFiresOnAChange(t *testing.T) {
	tracker := state.NewTracker()

	var mu sync.Mutex
	var calls int
	tracker.OnChange(func(s state.Snapshot) {
		mu.Lock()
		calls++
		mu.Unlock()
	})

	tracker.SetMuted(true)

	mu.Lock()
	got := calls
	mu.Unlock()
	if got != 1 {
		t.Fatalf("the listener fired %d times, want 1", got)
	}

	// Setting the same value again changes nothing, so nothing fires.
	tracker.SetMuted(true)

	mu.Lock()
	got = calls
	mu.Unlock()
	if got != 1 {
		t.Errorf("the listener fired %d times after a no-op change, want 1", got)
	}
}

// TestSnapshotDoesNotShareItsSlices checks that a caller cannot change the
// tracker's state through the snapshot it was handed.
func TestSnapshotDoesNotShareItsSlices(t *testing.T) {
	tracker := state.NewTracker()
	tracker.SetFeatures([]string{"listen_session"})

	snap := tracker.Snapshot()
	snap.Features[0] = "tampered"

	if tracker.Snapshot().Features[0] != "listen_session" {
		t.Error("the snapshot shared its slice with the tracker")
	}
}

// TestLinkDownSetsThePhase checks that a dropped link is visible.
func TestLinkDownSetsThePhase(t *testing.T) {
	tracker := state.NewTracker()
	tracker.SetLink(true)
	tracker.SetLink(false)

	snap := tracker.Snapshot()
	if snap.Phase != state.PhaseDisconnected {
		t.Errorf("the phase was %q, want %q", snap.Phase, state.PhaseDisconnected)
	}
	if snap.LinkUp {
		t.Error("the link was still reported as up")
	}
}
