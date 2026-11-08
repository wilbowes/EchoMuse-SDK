package vad_test

import (
	"testing"
	"time"

	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/vad"
)

// frameDuration is how much audio one test frame is.
const frameDuration = 100 * time.Millisecond

// speechFrames is how many 100ms frames make up a run of speech that lasts
// the detector's SpeechMs.
//
// The run is measured from its first frame, so 200ms of speech is three
// frames: the one it began on and the two that follow it.
const speechFrames = 3

// loud is a frame of speech: a tone at an amplitude well above the noise.
func loud(amplitude int16) []byte {
	pcm := make([]byte, 1600*2)
	for i := 0; i < 1600; i++ {
		var v int16
		if i%20 < 10 {
			v = amplitude
		} else {
			v = -amplitude
		}
		pcm[i*2] = byte(v)
		pcm[i*2+1] = byte(uint16(v) >> 8)
	}
	return pcm
}

// quiet is a frame of room noise: too small to be speech.
func quiet() []byte {
	return loud(10)
}

// testConfig is the configuration the tests use.
func testConfig() vad.Config {
	return vad.Config{
		Threshold:  3.0,
		SpeechMs:   200,
		SilenceMs:  5000,
		MaxTurn:    30 * time.Second,
		NoSpeechMs: 10000,
	}
}

// startSpeaking drives the detector through a run of speech and returns
// the moment the turn began.
func startSpeaking(t *testing.T, d *vad.Detector, now time.Time) time.Time {
	t.Helper()
	d.Push(quiet(), now)
	for i := 1; i < speechFrames; i++ {
		now = now.Add(frameDuration)
		d.Push(loud(3000), now)
	}
	now = now.Add(frameDuration)
	if got := d.Push(loud(3000), now); got != vad.Speaking {
		t.Fatalf("the turn did not start: %v", got)
	}
	return now
}

// TestSpeechStartsTheTurn checks that a turn waits for speech.
//
// The stream must not begin because the microphone opened. It begins when
// somebody is talking.
func TestSpeechStartsTheTurn(t *testing.T) {
	d := vad.New(testConfig())

	now := time.Now()
	d.Open(now)
	if got := d.Push(quiet(), now); got != vad.Silent {
		t.Errorf("a quiet frame gave %v, want silent", got)
	}

	// The run of speech has to last long enough to be a sentence.
	for i := 1; i < speechFrames; i++ {
		now = now.Add(frameDuration)
		if got := d.Push(loud(3000), now); got != vad.Silent {
			t.Errorf("frame %d of the run gave %v, want silent", i, got)
		}
	}

	now = now.Add(frameDuration)
	if got := d.Push(loud(3000), now); got != vad.Speaking {
		t.Errorf("the last frame of the run gave %v, want speaking", got)
	}
	if !d.Speaking() {
		t.Error("the detector does not consider the turn started")
	}
}

// TestABriefNoiseIsNotSpeech checks that a door is not a sentence.
//
// A single loud frame — a door, a cough, a dropped object — must not open
// a turn, or every loud noise in the room becomes a question to Home
// Assistant.
func TestABriefNoiseIsNotSpeech(t *testing.T) {
	d := vad.New(testConfig())

	now := time.Now()
	d.Open(now)
	d.Push(quiet(), now)
	now = now.Add(frameDuration)
	if got := d.Push(loud(8000), now); got != vad.Silent {
		t.Errorf("a single loud frame gave %v, want silent", got)
	}

	// And it stays not-speech afterwards.
	now = now.Add(frameDuration)
	if got := d.Push(quiet(), now); got != vad.Silent {
		t.Errorf("the frame after a click gave %v, want silent", got)
	}
	if d.Speaking() {
		t.Error("a click started the turn")
	}
}

// TestSilenceEndsTheTurn checks the five second rule.
//
// The turn ends after the silence has lasted long enough, so a pause in
// the middle of a sentence does not end it and the device does not stream
// a room indefinitely.
func TestSilenceEndsTheTurn(t *testing.T) {
	d := vad.New(testConfig())

	now := time.Now()
	d.Open(now)
	now = startSpeaking(t, d, now)

	// The silence is measured from the last frame of speech, so the turn
	// ends on the frame that is five seconds after it.
	for i := 0; i < 49; i++ {
		now = now.Add(frameDuration)
		if got := d.Push(quiet(), now); got != vad.Speaking {
			t.Fatalf("the turn ended after %v of silence, want it to last 5s", time.Duration(i+1)*frameDuration)
		}
	}

	// The fifth second ends it.
	now = now.Add(frameDuration)
	if got := d.Push(quiet(), now); got != vad.Quiet {
		t.Errorf("after 5s of silence the state was %v, want quiet", got)
	}
}

// TestAPauseDoesNotEndTheTurn checks that a sentence is not cut in half.
//
// A pause of a second in the middle of a sentence is not the end of the
// sentence, and the turn must survive it.
func TestAPauseDoesNotEndTheTurn(t *testing.T) {
	d := vad.New(testConfig())

	now := time.Now()
	d.Open(now)
	now = startSpeaking(t, d, now)

	// A second of silence.
	for i := 0; i < 10; i++ {
		now = now.Add(frameDuration)
		if got := d.Push(quiet(), now); got != vad.Speaking {
			t.Fatalf("the turn ended after %v of silence, want it to survive a pause",
				time.Duration(i+1)*frameDuration)
		}
	}

	// And the speech continues.
	now = now.Add(frameDuration)
	if got := d.Push(loud(3000), now); got != vad.Speaking {
		t.Errorf("the speech after the pause gave %v, want speaking", got)
	}
}

// TestTheTurnIsCapped checks that a controller cannot hold the microphone
// open.
//
// The cap is the device's own limit. A controller that never sends
// `mic_stop` must not keep the room streaming.
func TestTheTurnIsCapped(t *testing.T) {
	d := vad.New(testConfig())

	now := time.Now()
	d.Open(now)
	opened := now
	now = startSpeaking(t, d, now)

	// Someone keeps talking for longer than the cap.
	for i := 0; i < 400; i++ {
		now = now.Add(frameDuration)
		if got := d.Push(loud(3000), now); got == vad.TimedOut {
			if elapsed := now.Sub(opened); elapsed < 30*time.Second {
				t.Errorf("the turn timed out after %v, want at least 30s", elapsed)
			}
			return
		}
	}
	t.Error("the turn was never capped")
}

// TestATurnThatNeverHearsSpeechEnds checks the wait for somebody to
// speak.
//
// A controller that asks for the microphone and then says nothing has
// still taken the microphone, and the device gives it back.
func TestATurnThatNeverHearsSpeechEnds(t *testing.T) {
	d := vad.New(testConfig())

	now := time.Now()
	d.Open(now)

	// The wait for somebody to speak is ten seconds.
	for i := 0; i < 99; i++ {
		now = now.Add(frameDuration)
		if got := d.Push(quiet(), now); got == vad.NoSpeech {
			t.Fatalf("the turn ended without speech after %v", time.Duration(i+1)*frameDuration)
		}
	}
	now = now.Add(frameDuration)
	if got := d.Push(quiet(), now); got != vad.NoSpeech {
		t.Errorf("after 10s of silence the state was %v, want no speech", got)
	}
}

// TestTheNoiseFloorIsRelative checks that a noisy room still works.
//
// The threshold is relative to the floor the detector measures, so a room
// with a fan in it detects speech the same way a quiet one does.
func TestTheNoiseFloorIsRelative(t *testing.T) {
	d := vad.New(testConfig())

	// The room has a hum in it.
	now := time.Now()
	d.Open(now)
	for i := 0; i < 20; i++ {
		d.Push(loud(500), now)
		now = now.Add(frameDuration)
	}

	// Speech in that room is louder than the hum, and starts the turn.
	for i := 1; i < speechFrames; i++ {
		if got := d.Push(loud(4000), now); got != vad.Silent {
			t.Errorf("frame %d of the run gave %v, want silent", i, got)
		}
		now = now.Add(frameDuration)
	}
	if got := d.Push(loud(4000), now); got != vad.Speaking {
		t.Errorf("the last frame of the run gave %v, want speaking", got)
	}
}

// TestResetForgetsTheTurn checks that a capture restart clears the state.
//
// Audio from before a gap must not keep a turn alive, or a device that
// reopens its microphone would think the turn never ended.
func TestResetForgetsTheTurn(t *testing.T) {
	d := vad.New(testConfig())

	now := time.Now()
	d.Open(now)
	startSpeaking(t, d, now)

	d.Reset()

	if d.Speaking() {
		t.Error("the reset did not forget the turn")
	}
	now = now.Add(frameDuration)
	if got := d.Push(quiet(), now); got != vad.Silent {
		t.Errorf("after a reset the state was %v, want silent", got)
	}
}
