// Package turn bounds a microphone turn on the device's own limits.
//
// The controller asks for the microphone and may never say stop. A device
// that honours that request literally would stream the room for ever, so
// the turn is bounded here instead:
//
//   - nothing is sent until somebody is actually speaking;
//   - the turn ends when the speech stops;
//   - the turn ends if nobody speaks at all;
//   - the turn ends when it has run for as long as a turn may run.
//
// The limits are the device's, not the controller's, so a controller that
// fails mid-turn cannot leave the microphone open.
//
// This lives in the core rather than in a program so that every client
// bounds its turns the same way. A program that implements its own copy
// is a program whose privacy promise is only as good as its copy.
package turn

import (
	"log/slog"
	"time"

	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse"
	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/vad"
)

// Outcome is what happened to a turn.
type Outcome string

const (
	// OutcomeNone — the turn is still running, and the frame was not sent
	// because nobody was speaking yet.
	OutcomeNone Outcome = ""
	// OutcomeSent — the frame was sent because somebody was speaking.
	OutcomeSent Outcome = "sent"
	// OutcomeSpeechEnded — the speech stopped and the turn is over.
	OutcomeSpeechEnded Outcome = "speech_ended"
	// OutcomeNoSpeech — nobody spoke and the turn is over.
	OutcomeNoSpeech Outcome = "no_speech"
	// OutcomeTimedOut — the turn ran for as long as a turn may run.
	OutcomeTimedOut Outcome = "timed_out"
)

// Guard bounds one microphone turn.
//
// It is not safe for concurrent use: the audio path drives it from a
// single goroutine, the same way it drives the wake word scorer.
type Guard struct {
	device   *echomuse.Device
	detector *vad.Detector
	log      *slog.Logger

	// active is whether a bounded turn is running.
	active bool
	// opened is whether the turn has been opened, which is when the cap
	// starts to run.
	opened bool
}

// Options configures a Guard.
type Options struct {
	// Device is the link the turn runs on. It is required.
	Device *echomuse.Device
	// Detector decides what counts as speech. One is made with the default
	// configuration when this is nil.
	Detector *vad.Detector
	// Log receives the guard's own diagnostics. A nil logger discards them.
	Log *slog.Logger
}

// New returns a Guard.
func New(opts Options) *Guard {
	if opts.Device == nil {
		panic("turn: a device is required")
	}
	detector := opts.Detector
	if detector == nil {
		detector = vad.New(vad.Default())
	}
	log := opts.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Guard{device: opts.Device, detector: detector, log: log}
}

// Start opens a bounded turn.
//
// The turn is measured from this moment, so a turn that never sees anybody
// speaking is still bounded. Calling it while a turn is running replaces
// the turn, which is what happens when the controller asks again.
func (g *Guard) Start(now time.Time) {
	g.active = true
	g.opened = false
	g.detector.Open(now)
}

// Active reports whether a bounded turn is running.
func (g *Guard) Active() bool { return g.active }

// Push feeds one frame of audio to the guard and reports what happened.
//
// The frame is signed 16-bit little-endian mono at 16 kHz, the same audio
// the device sends to the controller.
//
// When the outcome is not OutcomeSent the frame was not sent, and the turn
// is over.
func (g *Guard) Push(pcm []byte, now time.Time) Outcome {
	if !g.active {
		return OutcomeNone
	}

	// The turn is opened once, when the controller asked for the
	// microphone, so the cap is measured from that moment.
	if !g.opened {
		g.detector.Open(now)
		g.opened = true
	}

	switch g.detector.Push(pcm, now) {
	case vad.Speaking:
		if err := g.device.SendMicAudio(pcm); err != nil {
			g.log.Debug("the audio did not reach the controller", "error", err)
			return OutcomeNone
		}
		return OutcomeSent

	case vad.Quiet:
		g.log.Info("the bounded turn ended after the speech stopped")
		if err := g.device.SendVADEnd(); err != nil {
			g.log.Debug("the end of the turn did not reach the controller", "error", err)
		}
		g.End()
		return OutcomeSpeechEnded

	case vad.NoSpeech:
		g.log.Info("the bounded turn ended with no speech")
		if err := g.device.SendNoSpeechTimeout(); err != nil {
			g.log.Debug("the end of the turn did not reach the controller", "error", err)
		}
		g.End()
		return OutcomeNoSpeech

	case vad.TimedOut:
		g.log.Info("the bounded turn reached its limit")
		if err := g.device.SendVADEnd(); err != nil {
			g.log.Debug("the end of the turn did not reach the controller", "error", err)
		}
		g.End()
		return OutcomeTimedOut
	}

	// Silent: the turn has not started, so nothing is sent.
	return OutcomeNone
}

// End finishes a bounded turn and gives the microphone back.
//
// The caller stops the capture, so a controller that never said `mic_stop`
// cannot keep the room.
func (g *Guard) End() {
	g.active = false
	g.opened = false
	g.detector.EndTurn()
}

// Detector returns the speech detector, so a program can report the level
// it is seeing.
func (g *Guard) Detector() *vad.Detector { return g.detector }
