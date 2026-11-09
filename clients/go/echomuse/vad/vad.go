// Package vad decides whether a microphone stream contains speech.
//
// It is the gate on a bounded turn: the controller asks for the microphone
// with `mic_start` and `lock_mic:true` — the user pressed a button, or Home
// Assistant asked a follow-up question — and the device answers with audio
// only once somebody is actually speaking. A turn that streams from the
// first frame sends the room to the controller before anyone has said
// anything.
//
// It is a small energy detector, not a neural one. It answers one
// question: is there speech in this frame of audio? That is enough to
// gate a turn and to end it, and it is small enough to run on a device
// with one core.
//
// # The rules it enforces
//
// A turn starts when speech is detected, and ends when the speech stops.
// Three limits bound it, and they are the device's own, so a controller
// that fails mid-turn cannot leave the microphone open:
//
//   - speech must last `SpeechMs` before the turn is considered started,
//     so a door closing is not a sentence;
//   - the turn ends after `SilenceMs` of nothing after the last speech,
//     so a pause in the middle of a sentence does not end it;
//   - the turn is capped at `MaxTurn`, so a controller that never says
//     stop cannot hold the microphone open.
//
// The cap covers the whole turn, including the wait for somebody to start
// speaking. A controller that asks for the microphone and then says
// nothing has still taken the microphone, and the device gives it back.
//
// # The threshold
//
// A frame counts as speech when it is louder than a threshold, and the
// threshold is learned from the frames that were not speech.
//
// The learning is one-directional on purpose. A frame that is not speech
// lowers the threshold toward the room's noise; a frame that is speech
// leaves it alone. So the threshold settles at the level of the room, and
// speech has to be louder than that to be heard — no matter whether the
// microphone opened during silence or in the middle of a sentence.
package vad

import (
	"math"
	"time"
)

// The defaults.
//
// They are the values the controller's own configuration uses, and they
// are here so a device that is not given a configuration still has
// sensible ones.
const (
	// DefaultThreshold is how far above the noise floor a frame has to be
	// to count as speech. It is a ratio, not a level.
	DefaultThreshold = 3.0
	// DefaultSpeechMs is how long speech has to last before the turn is
	// considered started.
	DefaultSpeechMs = 200
	// DefaultSilenceMs is how long the audio has to be silent after the
	// last speech before the turn ends.
	DefaultSilenceMs = 5000
	// DefaultMaxTurn is the longest a turn may last.
	DefaultMaxTurn = 30 * time.Second
	// DefaultNoSpeechMs is how long the device waits for somebody to
	// start speaking before it gives the microphone back.
	DefaultNoSpeechMs = 10000
)

// MinSpeechLevel is the level a frame has to reach to count as speech
// whatever the noise floor is.
//
// The floor is learned from the quiet frames, and the learning is
// one-directional: a frame that is not speech lowers it, and a frame
// that is speech leaves it alone. So the floor settles at the level of
// the room and speech has to be louder than that to be heard.
//
// The absolute level under it is what lets a microphone that opened in
// the middle of a sentence still hear the sentence: the floor starts at
// zero, and the first frame of a voice is above the absolute level even
// though nothing has been learned about the room yet.
const MinSpeechLevel = 600.0

// Default is the configuration a detector uses when it is not given one.
func Default() Config {
	return Config{
		Threshold:  DefaultThreshold,
		SpeechMs:   DefaultSpeechMs,
		SilenceMs:  DefaultSilenceMs,
		MaxTurn:    DefaultMaxTurn,
		NoSpeechMs: DefaultNoSpeechMs,
	}
}

// Config is how a detector decides what speech is.
type Config struct {
	// Threshold is how far above the noise floor a frame has to be to
	// count as speech. Zero means DefaultThreshold.
	Threshold float64
	// SpeechMs is how long speech has to last before the turn is
	// considered started. Zero means DefaultSpeechMs.
	SpeechMs int
	// SilenceMs is how long the audio has to be silent after the last
	// speech before the turn ends. Zero means DefaultSilenceMs.
	SilenceMs int
	// MaxTurn is the longest a turn may last. Zero means DefaultMaxTurn.
	MaxTurn time.Duration
	// NoSpeechMs is how long the device waits for somebody to start
	// speaking before it gives the microphone back. Zero means
	// DefaultNoSpeechMs.
	NoSpeechMs int
}

// withDefaults fills in the unset fields.
func (c Config) withDefaults() Config {
	if c.Threshold <= 0 {
		c.Threshold = DefaultThreshold
	}
	if c.SpeechMs <= 0 {
		c.SpeechMs = DefaultSpeechMs
	}
	if c.SilenceMs <= 0 {
		c.SilenceMs = DefaultSilenceMs
	}
	if c.MaxTurn <= 0 {
		c.MaxTurn = DefaultMaxTurn
	}
	if c.NoSpeechMs <= 0 {
		c.NoSpeechMs = DefaultNoSpeechMs
	}
	return c
}

// State is what the detector has concluded.
type State int

const (
	// Silent — no speech has been detected yet.
	Silent State = iota
	// Speaking — speech has been detected and has not yet ended.
	Speaking
	// Quiet — speech was detected and has since stopped. The turn is
	// ending.
	Quiet
	// TimedOut — the turn ran for longer than the cap.
	TimedOut
	// NoSpeech — the turn ended without any speech ever being detected.
	NoSpeech
)

// String names the state.
func (s State) String() string {
	switch s {
	case Speaking:
		return "speaking"
	case Quiet:
		return "quiet"
	case TimedOut:
		return "timed out"
	case NoSpeech:
		return "no speech"
	default:
		return "silent"
	}
}

// Detector watches a stream of audio and says when a turn should start
// and when it should end.
//
// It is not safe for concurrent use: the audio path drives it from a
// single goroutine, the same way it drives the wake word scorer.
type Detector struct {
	cfg Config

	// floor is the level the room's noise sits at, and speechLevel is the
	// level a frame has to beat to count as speech.
	floor float64
	// speechSince is when the current run of speech frames began, and
	// lastSpeech is when speech was last heard.
	speechSince time.Time
	lastSpeech  time.Time
	// opened is when the turn began, whether or not speech has started.
	opened time.Time
	// speaking is whether the turn has started.
	speaking bool
	// primed is whether a frame has been seen, so that the floor is not
	// compared against zero.
	primed bool
	// level is the most recent frame's level, as a percentage of full
	// scale. A level meter is drawn from it.
	level int
}

// New returns a Detector.
//
// The detector does not know when a turn began until it is told, which is
// what Open is for.
func New(cfg Config) *Detector {
	return &Detector{cfg: cfg.withDefaults()}
}

// Config returns the configuration in force, with the defaults filled in.
func (d *Detector) Config() Config { return d.cfg }

// Configure replaces the configuration in force.
//
// The new configuration takes effect from the next frame. The measured
// noise floor is kept, because it is a property of the room rather than of
// the settings, and relearning it would make the detector deaf for a
// second after every change.
//
// This is not safe for concurrent use: the audio path drives the detector
// from a single goroutine, the same way it drives the wake word scorer.
func (d *Detector) Configure(cfg Config) {
	d.cfg = cfg.withDefaults()
}

// Open begins a turn at the given moment.
//
// The cap is measured from here, so a turn that never sees anybody
// speaking is still bounded.
func (d *Detector) Open(now time.Time) {
	d.opened = now
	d.speaking = false
	d.speechSince = time.Time{}
	d.lastSpeech = time.Time{}
}

// Push feeds one frame of audio and reports what the detector concluded.
//
// The samples are signed 16-bit little-endian mono. The frame's level is
// compared with the noise floor, and the detector reports Speaking once
// speech has lasted long enough to be a sentence.
//
// A frame that is not speech does not end the turn by itself: the turn
// ends when the silence has lasted long enough, which is what Quiet says.
func (d *Detector) Push(pcm []byte, now time.Time) State {
	level := rms(pcm)
	d.level = percent(level)

	// The cap is checked first, because it applies to the whole turn:
	// whatever the audio is doing, the turn ends when it is over.
	if !d.opened.IsZero() && now.Sub(d.opened) >= d.cfg.MaxTurn {
		return TimedOut
	}

	if !d.primed {
		d.primed = true
		d.floor = 0
	}

	speaking := level >= d.speechLevel()
	if !speaking {
		// Only the quiet frames teach the detector what quiet is. The
		// floor falls toward them quickly and rises back very slowly, so
		// a room's noise is learned in a second or two and a brief loud
		// moment does not raise the bar above the speech after it.
		d.floor = d.updateFloor(level)
	}
	if d.speaking {
		if speaking {
			d.lastSpeech = now
			d.speechSince = time.Time{}
			return Speaking
		}
		if now.Sub(d.lastSpeech) >= d.silenceDuration() {
			return Quiet
		}
		return Speaking
	}

	if !speaking {
		// The run of speech was too short to be a sentence, so it never
		// was one.
		d.speechSince = time.Time{}
		// Nobody has spoken at all, and the wait for them has run out.
		if d.noSpeechExpired(now) {
			return NoSpeech
		}
		return Silent
	}

	// A frame of speech. The turn starts only once the speech has lasted
	// long enough, which is what separates a sentence from a door.
	if d.speechSince.IsZero() {
		d.speechSince = now
	}
	if now.Sub(d.speechSince) < d.speechDuration() {
		return Silent
	}
	d.speaking = true
	d.lastSpeech = now
	d.speechSince = time.Time{}
	return Speaking
}

// updateFloor moves the noise floor toward a frame that was not speech.
//
// The floor falls quickly and rises slowly. It falls fast so that a
// room's noise is learned within a second or two of the microphone
// opening, and it rises slowly so that a brief loud moment does not
// raise the bar above the speech that follows it.
//
// Only the frames that were not speech reach this function. A frame of
// speech leaves the floor alone, so a floor that rose to meet the voice
// would never happen — which is what would stop a microphone that
// opened mid-sentence from hearing the rest of it.
func (d *Detector) updateFloor(level float64) float64 {
	if level < d.floor {
		return d.floor*0.9 + level*0.1
	}
	return d.floor*0.99 + level*0.01
}

// speechLevel is the level a frame has to reach to count as speech.
func (d *Detector) speechLevel() float64 {
	level := d.floor * d.cfg.Threshold
	if level < MinSpeechLevel {
		level = MinSpeechLevel
	}
	return level
}

// noSpeechExpired reports whether the wait for somebody to speak is over.
func (d *Detector) noSpeechExpired(now time.Time) bool {
	if d.opened.IsZero() {
		return false
	}
	return now.Sub(d.opened) >= d.noSpeechDuration()
}

// speechDuration is how long speech has to last to start a turn.
func (d *Detector) speechDuration() time.Duration {
	return time.Duration(d.cfg.SpeechMs) * time.Millisecond
}

// silenceDuration is how long the silence has to last to end a turn.
func (d *Detector) silenceDuration() time.Duration {
	return time.Duration(d.cfg.SilenceMs) * time.Millisecond
}

// noSpeechDuration is how long the device waits for somebody to speak.
func (d *Detector) noSpeechDuration() time.Duration {
	return time.Duration(d.cfg.NoSpeechMs) * time.Millisecond
}

// Reset forgets the turn and the measured noise floor.
//
// It is used when a capture restarts, so the audio from before the gap
// cannot keep a turn alive.
func (d *Detector) Reset() {
	d.floor = 0
	d.primed = false
	d.speaking = false
	d.opened = time.Time{}
	d.speechSince = time.Time{}
	d.lastSpeech = time.Time{}
}

// Speaking reports whether the detector considers the turn to have started.
func (d *Detector) Speaking() bool { return d.speaking }

// Level is the level of the most recent frame, on a scale of 0 to 100.
//
// It is the level of the most recent frame rather than a running maximum,
// so a meter drawn from it follows the room instead of sticking at the
// loudest thing that ever happened. It is zero before any frame has been
// seen.
//
// The scale is in decibels below full scale, so the quiet end of it is
// stretched out rather than the loud end. A room that is quiet still moves
// the meter, which is what makes it readable.
//
// The detector is the only place that sees every frame, including the ones
// before the speech starts, so it is the only place a level meter can be
// fed from.
func (d *Detector) Level() int {
	return d.level
}

// EndTurn forgets the turn but keeps the measured noise floor.
//
// The floor is a property of the room, not of the turn, so a turn that
// ends and a new one that starts should not have to relearn it.
func (d *Detector) EndTurn() {
	d.speaking = false
	d.opened = time.Time{}
	d.speechSince = time.Time{}
	d.lastSpeech = time.Time{}
}

// rms is the root mean square of the samples, as a level.
//
// It is a level rather than a peak, because a peak follows a single click
// and a room's noise is better described by its average energy.
func rms(pcm []byte) float64 {
	if len(pcm) < 2 {
		return 0
	}
	var sum float64
	count := 0
	for i := 0; i+1 < len(pcm); i += 2 {
		v := int16(uint16(pcm[i]) | uint16(pcm[i+1])<<8)
		f := float64(v)
		sum += f * f
		count++
	}
	if count == 0 {
		return 0
	}
	return math.Sqrt(sum / float64(count))
}

// percent is a level as a percentage of full scale.
//
// The scale is in decibels rather than in amplitude, because that is how a
// level is read: a quiet room and a loud one differ by a ratio, not by a
// difference, and an amplitude scale spends almost all of its range on
// sounds that are too loud to happen.
//
// The bottom of the scale is 60 dB below full scale, which is quieter than
// the noise of a quiet room, and the top is full scale itself. So a meter
// drawn from it spends its travel on the range a person is actually in.
func percent(level float64) int {
	if level <= 0 {
		return 0
	}
	decibels := 20 * math.Log10(level/32767)
	p := int((decibels + 60) * 100 / 60)
	if p < 0 {
		return 0
	}
	if p > 100 {
		return 100
	}
	return p
}
