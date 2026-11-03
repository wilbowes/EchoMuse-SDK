package emulator

import (
	"time"

	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/protocol"
)

// Drain waits for queued response audio where the backend can report it.
func (s *speakerHandle) Drain() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d, ok := s.w.(PlaybackDrainer); ok {
		return d.Drain()
	}
	return nil
}

// finishSpeakerResponse reports completion only after queued voice samples
// have reached the output. The controller uses this event to release speaking
// state and its music duck without waiting for its conservative timeout.
func (e *Emulator) finishSpeakerResponse() {
	e.mu.Lock()
	musicActive := e.voiceMixed
	speaker := e.speaker
	e.mu.Unlock()
	if musicActive {
		e.waitVoiceMix()
		e.clearVoiceMix()
		// The shared PCM stream has a short hardware queue after the final mixed
		// period. Let it clear before telling the controller playback is done.
		time.Sleep(120 * time.Millisecond)
	} else if speaker != nil {
		if err := speaker.Drain(); err != nil {
			e.log.Debug("speaker drain failed", "error", err)
		}
		e.StopPlayback()
	}
	if err := e.device.SendPlaybackStats(protocol.PlaybackStats{}); err != nil {
		e.log.Debug("playback completion did not reach controller", "error", err)
	} else {
		e.log.Info("voice playback drained; completion reported to controller")
	}
}

func (e *Emulator) queueVoiceMix(pcm []byte) {
	e.voiceMixMu.Lock()
	e.voiceMix = append(e.voiceMix, pcm...)
	e.voiceMixMu.Unlock()
}

func (e *Emulator) takeVoiceMix(dst []byte) {
	e.voiceMixMu.Lock()
	clear(dst)
	n := copy(dst, e.voiceMix)
	e.voiceMix = e.voiceMix[n:]
	if len(e.voiceMix) == 0 {
		e.voiceMix = nil
	}
	e.voiceMixCond.Broadcast()
	e.voiceMixMu.Unlock()
}

func (e *Emulator) waitVoiceMix() {
	e.mu.Lock()
	ctx := e.ctx
	e.mu.Unlock()
	e.voiceMixMu.Lock()
	wake := make(chan struct{})
	if ctx != nil {
		go func() {
			select {
			case <-ctx.Done():
				e.voiceMixMu.Lock()
				e.voiceMixCond.Broadcast()
				e.voiceMixMu.Unlock()
			case <-wake:
			}
		}()
	}
	for len(e.voiceMix) > 0 && (ctx == nil || ctx.Err() == nil) {
		e.voiceMixCond.Wait()
	}
	close(wake)
	e.voiceMixMu.Unlock()
}

func (e *Emulator) clearVoiceMix() {
	e.voiceMixMu.Lock()
	e.voiceMix = nil
	e.voiceMixCond.Broadcast()
	e.voiceMixMu.Unlock()
}
