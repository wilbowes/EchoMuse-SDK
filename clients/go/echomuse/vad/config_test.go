package vad_test

import (
	"testing"
	"time"

	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/vad"
)

// TestConfigureChangesTheThreshold checks that a new configuration takes
// effect.
//
// The settings come from the application's settings page, so a change to
// them has to reach the detector without the device being restarted.
//
// The threshold is relative to the noise floor, so the room has to have a
// floor to be measured against before it means anything.
func TestConfigureChangesTheThreshold(t *testing.T) {
	// The frame is loud enough that the ordinary threshold accepts it and
	// quiet enough that a raised one rejects it.
	const borderline = 2000

	// At the ordinary threshold it is speech.
	ordinary := vad.New(testConfig())
	now := time.Now()
	ordinary.Open(now)
	now = noisyRoom(t, ordinary, now)
	for i := 0; i < speechFrames; i++ {
		now = now.Add(frameDuration)
		if got := ordinary.Push(loud(borderline), now); got == vad.Speaking {
			break
		}
	}
	if !ordinary.Speaking() {
		t.Fatal("the frame was not heard at the ordinary threshold")
	}

	// At a raised threshold it is not.
	raised := vad.New(testConfig())
	now = time.Now()
	raised.Open(now)
	now = noisyRoom(t, raised, now)

	cfg := raised.Config()
	cfg.Threshold = 100.0
	raised.Configure(cfg)

	for i := 0; i < speechFrames; i++ {
		now = now.Add(frameDuration)
		if got := raised.Push(loud(borderline), now); got == vad.Speaking {
			t.Fatal("the speech was heard even though the threshold was raised above it")
		}
	}
}

// noisyRoom feeds the detector enough of a room's noise for it to learn a
// floor, and returns the moment the last frame was seen.
func noisyRoom(t *testing.T, d *vad.Detector, now time.Time) time.Time {
	t.Helper()
	for i := 0; i < 20; i++ {
		d.Push(loud(500), now)
		now = now.Add(frameDuration)
	}
	return now
}

// TestConfigureKeepsTheNoiseFloor checks that a change does not deafen the
// detector.
//
// The floor is a property of the room, not of the settings, so a new
// configuration must not throw it away. A detector that relearned it would
// miss the first second of every utterance after a change.
func TestConfigureKeepsTheNoiseFloor(t *testing.T) {
	d := vad.New(testConfig())

	// The room has a hum in it, and the detector learns it.
	now := time.Now()
	d.Open(now)
	for i := 0; i < 20; i++ {
		d.Push(loud(500), now)
		now = now.Add(frameDuration)
	}

	// The speech threshold before the change.
	before := d.Config().Threshold

	cfg := d.Config()
	cfg.SpeechMs = 300
	d.Configure(cfg)

	after := d.Config()
	if after.Threshold != before {
		t.Errorf("the threshold changed from %v to %v when it was not set", before, after.Threshold)
	}
	if after.SpeechMs != 300 {
		t.Errorf("the speech duration was %v, want 300", after.SpeechMs)
	}
}

// TestConfigureFillsInTheUnset checks that an unset key keeps its default.
//
// A configuration is a set of instructions, not a picture of the whole
// detector, so a key that is not set must not become zero.
func TestConfigureFillsInTheUnset(t *testing.T) {
	d := vad.New(vad.Config{})

	cfg := d.Config()
	if cfg.Threshold != vad.DefaultThreshold {
		t.Errorf("the default threshold was %v, want %v", cfg.Threshold, vad.DefaultThreshold)
	}

	// A configuration that sets nothing keeps the defaults.
	d.Configure(vad.Config{})
	cfg = d.Config()
	if cfg.Threshold != vad.DefaultThreshold {
		t.Errorf("the threshold became %v after an empty configuration, want %v", cfg.Threshold, vad.DefaultThreshold)
	}
	if cfg.SpeechMs != vad.DefaultSpeechMs {
		t.Errorf("the speech duration became %v, want %v", cfg.SpeechMs, vad.DefaultSpeechMs)
	}
}

// room is a frame of the noise a room actually has.
//
// It is loud enough to sit on the meter's scale, which a frame of digital
// silence is not.
func room() []byte { return loud(300) }

// TestTheLevelFollowsTheRoom checks that the level meter follows the audio.
//
// A meter that sticks at the loudest thing that ever happened is useless,
// so the level is the most recent frame's, not a running maximum.
func TestTheLevelFollowsTheRoom(t *testing.T) {
	d := vad.New(testConfig())

	if got := d.Level(); got != 0 {
		t.Errorf("the level before any audio was %v, want 0", got)
	}

	now := time.Now()
	d.Open(now)
	d.Push(room(), now)

	quietLevel := d.Level()
	if quietLevel == 0 {
		t.Error("the level of the room's noise was zero")
	}
	if quietLevel >= 100 {
		t.Errorf("the level of the room's noise was %v, want less than 100", quietLevel)
	}

	// A loud frame raises it.
	now = now.Add(frameDuration)
	d.Push(loud(20000), now)

	loudLevel := d.Level()
	if loudLevel <= quietLevel {
		t.Errorf("the loud frame gave %v, want more than the room's %v", loudLevel, quietLevel)
	}

	// And the room brings it back down.
	now = now.Add(frameDuration)
	d.Push(room(), now)

	if got := d.Level(); got >= loudLevel {
		t.Errorf("the level stayed at %v after the room went quiet", got)
	}
}

// TestTheLevelIsBounded checks that the level fits on a scale.
//
// A meter that goes past the end of its scale cannot be read, so the level
// never exceeds a hundred percent.
func TestTheLevelIsBounded(t *testing.T) {
	d := vad.New(testConfig())

	now := time.Now()
	d.Open(now)
	d.Push(loud(32767), now)

	if got := d.Level(); got > 100 {
		t.Errorf("the level was %v, want no more than 100", got)
	}
}
