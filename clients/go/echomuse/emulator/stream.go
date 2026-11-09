package emulator

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/protocol"
	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/turn"
	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/wakeword"
)

// The rates the controller expects. Capture and playback differ, and a
// device that gets them wrong plays audio at the wrong speed.
const (
	// CaptureRate is what the controller expects captured audio at.
	CaptureRate = protocol.MicSampleRate
	// PlaybackRate is what the controller sends audio at.
	PlaybackRate = protocol.SpeakerSampleRate
)

// StartCapture streams the machine's microphone to the controller.
//
// The stream begins when the controller asks for it and stops when it says
// to stop, which is the order the protocol expects: a device that streams
// before it is asked is a device streaming to nobody.
func (e *Emulator) StartCapture(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	e.mu.Lock()
	if e.capturing || e.muted {
		e.mu.Unlock()
		cancel()
		return
	}
	e.capturing = true
	e.captureGen++
	gen := e.captureGen
	audio := e.audio
	e.captureCancel = cancel
	e.mu.Unlock()
	if e.wakeListener != nil {
		e.wakeListener.Reset()
	}

	e.log.Info("the controller asked for the microphone")

	go func() {
		defer func() {
			e.mu.Lock()
			var endedSession uint32
			// Only the capture that is current may clear the flag: a
			// newer one has taken over by the time this one finishes.
			if e.captureGen == gen {
				e.capturing = false
				e.captureCancel = nil
				endedSession, e.wakeSession = e.wakeSession, 0
			}
			e.mu.Unlock()
			if endedSession != 0 {
				_ = e.device.EndListenSession(endedSession, "link")
			}
			cancel()
		}()

		if e.opts.MuteCapture {
			// A demonstration where the room should not be heard. The
			// stream still runs, so the controller's flow is the same.
			e.log.Info("the microphone is muted for this run")
			<-ctx.Done()
			return
		}

		reader, err := audio.Capture(ctx, CaptureRate)
		if err != nil {
			e.log.Error("the microphone could not be opened", "error", err)
			e.setPhase("Microphone unavailable", err.Error())
			return
		}

		// The reader is held so that stopping the capture closes it.
		//
		// A capture that is only marked as stopped leaves the sound card
		// open, and the next capture opens a second one. The device then
		// streams whichever of the two the audio server gave it, and the
		// controller hears silence while the window says it is
		// listening.
		handle := &captureHandle{r: reader}
		e.mu.Lock()
		if e.captureGen != gen {
			e.mu.Unlock()
			handle.Close()
			return
		}
		e.capture = handle
		e.mu.Unlock()
		defer func() {
			e.mu.Lock()
			if e.capture == handle {
				e.capture = nil
			}
			e.mu.Unlock()
			handle.Close()
		}()

		// 100ms of audio at 16 kHz is 3200 bytes, which is one of the
		// capture frames' natural size.
		buf := make([]byte, CaptureRate/10*2)
		for {
			n, err := reader.Read(buf)
			if n > 0 {
				e.setLevel(peakLevel(buf[:n]))
				if !e.pushCaptureFrame(buf[:n]) {
					return
				}
			}
			if err != nil {
				if ctx.Err() == nil {
					e.log.Warn("the microphone stopped", "error", err)
					if reason := e.captureReason(); reason != "" {
						e.setPhase("Microphone unavailable", reason)
					}
				}
				return
			}
		}
	}()
}

// pushCaptureFrame sends one frame of captured audio to the controller.
//
// It reports whether the capture should continue. A bounded turn that
// has ended by its own rules stops the capture: the turn is over, and a
// device that kept streaming would be sending the room to a controller
// that has already moved on.
func (e *Emulator) pushCaptureFrame(pcm []byte) bool {
	e.mu.Lock()
	muted := e.muted
	wakeMode, session := e.wakeMode, e.wakeSession
	bounded := e.turnLockMic
	e.mu.Unlock()
	if muted {
		return true
	}

	// A bounded turn replaces the streaming for the length of the turn.
	//
	// `mic_start` with `lock_mic:true` replaces the local wake stream for
	// the length of the turn, so the turn's own rules — speech to start
	// it, silence to end it — are the ones that apply to what is sent.
	//
	// The wake word is still scored. The device keeps listening locally
	// during the turn, which is what hears a barge-in over the reply, and
	// a device that stopped scoring would miss it.
	if bounded {
		e.scoreWakeWord(pcm)
		return e.pushBoundedFrame(pcm)
	}

	// The wake word is scored on the device, so the microphone is open
	// whatever the controller asked for.
	if wakeMode != "" && e.wakeListener != nil {
		e.scoreWakeWord(pcm)

		e.mu.Lock()
		currentSession, currentMode, currentTurn := e.wakeSession, e.wakeMode, e.turnStream
		e.mu.Unlock()

		if currentMode == "shadow" || currentTurn {
			if sendErr := e.device.SendMicAudio(pcm); sendErr != nil {
				e.log.Debug("the audio did not reach the controller", "error", sendErr)
				return false
			}
			return true
		}
		if currentSession != 0 && currentSession == session {
			if sendErr := e.device.SendListenAudio(currentSession, pcm); sendErr != nil {
				e.log.Debug("private audio did not reach the controller", "error", sendErr)
				return false
			}
		}
		return true
	}

	if sendErr := e.device.SendMicAudio(pcm); sendErr != nil {
		e.log.Debug("the audio did not reach the controller", "error", sendErr)
		return false
	}
	return true
}

// scoreWakeWord scores one frame on the device's own wake word.
//
// It is a no-op when the device is not scoring locally.
func (e *Emulator) scoreWakeWord(pcm []byte) {
	e.mu.Lock()
	listener := e.wakeListener
	e.mu.Unlock()
	if listener == nil {
		return
	}
	if _, err := listener.Push(pcm); err != nil && err != wakeword.ErrNotReady {
		e.log.Error("local wakeword inference failed", "error", err)
	}
}

// pushBoundedFrame handles one frame of a bounded turn.
//
// The turn is gated on speech: nothing is sent until somebody is talking.
// It ends on its own when the speech stops, when nobody spoke at all, or
// when it has run for as long as a turn may run. The controller is told
// which of those happened, with the sentinel the protocol defines.
//
// The limits live in the core's turn package, so this program and every
// other client bound a turn the same way.
func (e *Emulator) pushBoundedFrame(pcm []byte) bool {
	switch e.turnGuard.Push(pcm, time.Now()) {
	case turn.OutcomeSent:
		return true
	case turn.OutcomeNone:
		// Silent: the turn has not started, so nothing is sent.
		return true
	default:
		// The turn is over, whatever ended it.
		e.endBoundedTurn()
		return false
	}
}

// endBoundedTurn finishes a bounded turn and gives the microphone back.
//
// The capture stops, so a controller that never said `mic_stop` cannot
// keep the room.
func (e *Emulator) endBoundedTurn() {
	e.mu.Lock()
	e.turnStream = false
	e.turnLockMic = false
	e.mu.Unlock()

	e.turnGuard.End()
	e.StopCapture()
	e.ReportListenState()
}

// StopCapture stops streaming the microphone.
func (e *Emulator) StopCapture() {
	e.mu.Lock()
	e.capturing = false
	e.captureGen++
	session := e.wakeSession
	e.wakeSession = 0
	muted := e.muted
	capture := e.capture
	e.capture = nil
	cancel := e.captureCancel
	e.captureCancel = nil
	e.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if capture != nil {
		capture.Close()
	}
	if session != 0 {
		reason := "capture_stopped"
		if muted {
			reason = "muted"
		}
		_ = e.device.EndListenSession(session, reason)
	}
	e.log.Info("microphone capture stopped")
}

// captureHandle is a live microphone stream.
type captureHandle struct {
	r    io.ReadCloser
	once sync.Once
}

// Close stops the microphone.
func (h *captureHandle) Close() {
	h.once.Do(func() { h.r.Close() })
}

// captureReason says why the microphone stopped, as the audio backend
// knows it.
//
// The backend reports ALSA's device error, which identifies the cause more clearly than the stream read error.
func (e *Emulator) captureReason() string {
	reporter, ok := e.audio.(FailureReporter)
	if !ok {
		return ""
	}
	return reporter.LastError()
}

// setLevel records the loudest sample in the last block of audio.
func (e *Emulator) setLevel(level int) {
	e.mu.Lock()
	changed := e.level != level
	e.level = level
	e.mu.Unlock()

	if changed {
		e.notifyLocked()
	}
}

// Level returns the loudest sample seen recently, as a percentage of full
// scale.
//
// It is the window's answer to "is the microphone working": a level of
// zero is a microphone that is open and hearing nothing, and a level of
// nothing at all is a microphone that is not open.
func (e *Emulator) Level() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.level
}

// peakLevel returns the loudest sample in a block of signed 16-bit
// little-endian samples, as a percentage of full scale.
func peakLevel(pcm []byte) int {
	peak := 0
	for i := 0; i+1 < len(pcm); i += 2 {
		v := int(int16(pcm[i]) | int16(pcm[i+1])<<8)
		if v < 0 {
			v = -v
		}
		if v > peak {
			peak = v
		}
	}
	return peak * 100 / 32768
}

// StartPlayback opens the machine's speaker.
//
// The stream is opened on the first chunk of a response and kept open
// until the response ends, because opening a sound card per response
// costs more than the response does.
func (e *Emulator) StartPlayback(ctx context.Context) {
	e.mu.Lock()
	if e.speaker != nil {
		e.mu.Unlock()
		return
	}
	if e.voiceMixed && e.sendspinOutput != nil && e.sendspin != nil && e.sendspin.Active() {
		// The clocked Sendspin output is the shared voice/music mixer.
		e.mu.Unlock()
		return
	}
	writer, err := e.audio.Playback(ctx, PlaybackRate)
	if err != nil {
		e.mu.Unlock()
		e.log.Error("the speaker could not be opened", "error", err)
		e.setPhase("Speaker unavailable", err.Error())
		return
	}

	e.speaker = &speakerHandle{w: writer}
	e.mu.Unlock()
}

// StopPlayback closes the machine's speaker.
func (e *Emulator) StopPlayback() {
	e.mu.Lock()
	speaker := e.speaker
	e.speaker = nil
	e.mu.Unlock()

	if speaker != nil {
		speaker.Close()
	}
}

// TapButton reports a press of the action button.
//
// It is what a tap on an emulator's window does, and it is the way to
// start a voice turn without the wake word.
func (e *Emulator) TapButton() {
	if err := e.device.SendButton(protocol.ButtonUp, 80, e.device.State().Snapshot().Muted); err != nil {
		e.log.Warn("the press did not reach the controller", "error", err)
		return
	}
	e.log.Info("pressed the action button")
}

// ReportListenState tells the controller what the emulator is doing with
// its microphone.
//
// The emulator streams to the controller, so that is what it says. The
// controller shows it honestly, and the dashboard labels such a device as
// streaming.
//
// A device that is streaming because of a bounded turn says so too. A
// device that claimed to be listening privately while it was sending the
// room would be making a statement it is not true of, and the dashboard
// is where a user reads whether their device is listening.
func (e *Emulator) ReportListenState() {
	e.mu.Lock()
	mode := e.wakeMode
	bounded := e.turnLockMic
	e.mu.Unlock()

	listen, reason := protocol.ListenStream, "the emulator streams to the controller"
	if mode == "local" {
		listen, reason = protocol.ListenLocal, "wakeword is scored locally; no microphone audio streams before a crossing"
	}
	if mode == "shadow" {
		listen, reason = protocol.ListenStream, "shadow wakeword scoring is active while audio streams to the controller"
	}
	if bounded {
		listen, reason = protocol.ListenStream, "a bounded turn is streaming the speech that answers the question"
	}
	if err := e.device.SendListenState(listen, reason); err != nil {
		e.log.Debug("the listen state did not reach the controller", "error", err)
	}
}

// logger returns the emulator's logger, for a caller that wants it.
func (e *Emulator) logger() *slog.Logger { return e.log }
