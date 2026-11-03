package emulator

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/protocol"
	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/wakeword"
)

func (e *Emulator) applyWakeConfig(cfg protocol.Config) {
	if e.localWake == nil {
		e.ReportListenState()
		return
	}
	mode := ""
	if cfg.WakeWordEnabled == nil || *cfg.WakeWordEnabled {
		switch cfg.OwwOnDevice {
		case protocol.WakeOnDevice:
			mode = "local"
		case protocol.WakeShadow:
			mode = "shadow"
		}
	}
	if mode != "" && cfg.OwwModel != "" && !sameModel(cfg.OwwModel, e.localWake.ModelName) {
		e.setWakeMode("")
		reason := fmt.Sprintf("loaded model %q does not match controller model %q", e.localWake.ModelName, cfg.OwwModel)
		_ = e.device.SendListenState(protocol.ListenDegraded, reason)
		e.log.Warn("local wakeword disabled because the configured model is not loaded", "reason", reason)
		return
	}
	if e.opts.MuteCapture {
		e.setWakeMode("")
		_ = e.device.SendListenState(protocol.ListenDegraded, "microphone capture is disabled for this emulator run")
		return
	}
	if mode == "" {
		e.setWakeMode("")
		reason := "wakeword is disabled; this device waits for a button or controller mic request"
		_ = e.device.SendListenState(protocol.ListenDegraded, reason)
		return
	}
	if cfg.OwwThreshold > 0 {
		e.wakeListener.Configure(float32(cfg.OwwThreshold), mode == "shadow")
	} else {
		e.wakeListener.Configure(e.localWake.Threshold, mode == "shadow")
	}
	e.setWakeMode(mode)
	if mode == "local" {
		_ = e.device.SendListenState(protocol.ListenLocal, "wakeword is scored on this device; microphone audio stays local until a crossing")
	} else {
		_ = e.device.SendListenState(protocol.ListenStream, "shadow wakeword scoring is active while audio streams to the controller")
	}
	e.mu.Lock()
	ctx, capturing := e.ctx, e.capturing
	e.mu.Unlock()
	if ctx != nil && !capturing {
		e.StartCapture(ctx)
	}
}

func (e *Emulator) setWakeMode(mode string) {
	e.mu.Lock()
	wasLocal := e.wakeMode != ""
	e.wakeMode = mode
	if mode == "" {
		e.turnStream = false
	}
	ctx, capturing := e.ctx, e.capturing
	e.mu.Unlock()
	if mode == "" && wasLocal && capturing {
		e.StopCapture()
		return
	}
	if mode != "" && ctx != nil && !capturing {
		e.StartCapture(ctx)
	}
}

func sameModel(configured, loaded string) bool {
	base := func(s string) string {
		s = filepath.Base(strings.TrimSpace(s))
		return strings.TrimSuffix(s, filepath.Ext(s))
	}
	return strings.EqualFold(base(configured), base(loaded))
}

func (e *Emulator) onWakeCrossing(c wakeword.Crossing, act bool) {
	e.mu.Lock()
	mode := e.wakeMode
	e.mu.Unlock()
	if mode == "shadow" || !act {
		_ = e.device.SendOWWShadowCross(c.Score, 0)
		return
	}
	if mode != "local" {
		return
	}
	session, err := e.device.StartListenSession(protocol.OWWWake{
		Score: c.Score, Threshold: c.Threshold, AgeMs: 80,
	})
	if err != nil {
		e.log.Warn("private wake could not open a session", "error", err)
		return
	}
	e.mu.Lock()
	e.wakeSession = session
	e.mu.Unlock()
	preRoll := e.wakeListener.Detector.RecentAudio(wakeword.ScoreSpan)
	const frameBytes = CaptureRate / 10 * 2
	for len(preRoll) > 0 {
		n := min(frameBytes, len(preRoll))
		if n%2 != 0 {
			n--
		}
		if n == 0 {
			break
		}
		if err := e.device.SendListenAudio(session, preRoll[:n]); err != nil {
			e.log.Warn("private wake pre-roll did not reach the controller", "error", err)
			_ = e.device.EndListenSession(session, "link")
			e.mu.Lock()
			if e.wakeSession == session {
				e.wakeSession = 0
			}
			e.mu.Unlock()
			return
		}
		preRoll = preRoll[n:]
	}
}

func (e *Emulator) localListenClosed(m protocol.ListenClose) {
	e.mu.Lock()
	if e.wakeSession == m.Session {
		e.wakeSession = 0
	}
	e.mu.Unlock()
	e.log.Info("private-listening session closed", "session", m.Session, "reason", m.Reason)
}
