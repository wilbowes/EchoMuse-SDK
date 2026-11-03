package echomuse

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/protocol"
	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/state"
)

const (
	listenAckTimeout = 3 * time.Second
	listenMaxOpen    = 30 * time.Second
)

// websocketTextMessage is the frame type of a text WebSocket message.
const websocketTextMessage = 1

// register sends the registration and waits for the controller's answer.
//
// The registration is the device's whole negotiation with the controller:
// the identity, the capabilities it gates every feature on, and the facts
// about the boot. The reply is an ack, a pending or a refused, and the
// difference matters — a pending device is waiting for a person, and a
// refused one has the wrong credentials.
func (d *Device) register(ctx context.Context, p *plane) error {
	msg := protocol.Register{
		Type:         protocol.TypeRegister,
		DeviceID:     d.opts.DeviceID,
		Version:      d.opts.Version,
		Capabilities: d.Capabilities(),
		BaseOS:       d.opts.BaseOS,
		Board:        d.opts.Board,
	}

	// The pairing flag says the device is asking to be admitted, rather
	// than being a device the controller has met before.
	//
	// It is set only during a pairing window. A device that has been
	// approved already does not need it: the approval is what issues the
	// link credential, and a device asking again opens a window that
	// outlives the moment anyone wanted it.
	if d.pairing {
		msg.Pairing = true
	}

	if err := p.writeJSON(msg); err != nil {
		return fmt.Errorf("echomuse: sending the registration: %w", err)
	}

	// The reply arrives on the same socket, and the read loop that would
	// normally handle it has not started yet — so it is read here. A
	// deadline bounds the wait: a controller that accepts the connection
	// and then says nothing must not hang the device forever.
	type reply struct {
		Type     string          `json:"type"`
		DeviceID string          `json:"device_id"`
		Features []string        `json:"features"`
		Pairing  bool            `json:"pairing"`
		Raw      json.RawMessage `json:"-"`
	}

	replyCh := make(chan reply, 1)
	errCh := make(chan error, 1)

	go func() {
		for {
			msgType, data, err := p.conn.ReadMessage()
			if err != nil {
				errCh <- err
				return
			}
			if msgType != websocketTextMessage {
				continue
			}
			var r reply
			if err := json.Unmarshal(data, &r); err != nil {
				continue
			}
			r.Raw = data
			replyCh <- r
			return
		}
	}()

	timeout := time.NewTimer(15 * time.Second)
	defer timeout.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errCh:
		return fmt.Errorf("echomuse: the controller closed the connection during registration: %w", err)
	case <-timeout.C:
		return errors.New("echomuse: the controller did not answer the registration")
	case r := <-replyCh:
		switch r.Type {
		case protocol.TypeAck:
			d.mu.Lock()
			d.features = map[string]bool{}
			for _, f := range r.Features {
				d.features[f] = true
			}
			d.mu.Unlock()
			d.state.SetFeatures(r.Features)
			d.state.SetApproved(true)
			d.log.Info("registered", "features", r.Features)
			return nil
		case protocol.TypePending:
			d.state.SetPhase(state.PhasePending, "waiting for an administrator to approve this device")
			d.state.SetApproved(false)
			// The controller closes the connection after this, so the
			// device redials. That retry is how it picks up the
			// approval, which is why the wait is short rather than
			// the full backoff.
			return ErrPendingApproval
		case protocol.TypeRefused:
			d.state.SetPhase(state.PhaseRefused, "the controller refused this device's credentials")
			d.state.SetApproved(false)
			return errors.New("echomuse: the controller refused this device's credentials")
		default:
			return fmt.Errorf("echomuse: unexpected reply to the registration: %q", r.Type)
		}
	}
}

// SendButton reports a press or release of the action button.
//
// The release carries the duration the press lasted, measured on the
// device. The controller's gesture decision is built on it: a hold is
// forwarded to Home Assistant as an event, and a tap starts a voice turn.
//
// The click type must be the action button's own code. The controller
// recognises one value, and a device that reports a different one has its
// presses ignored.
func (d *Device) SendButton(down bool, heldMs int64, muted bool) error {
	return d.sendControl(protocol.ButtonEvent{
		Type:      protocol.TypeButton,
		ClickType: protocol.ButtonClickType,
		Down:      down,
		HeldMs:    heldMs,
		Muted:     muted,
	})
}

// SendMuteState reports the device's mute switch.
//
// Mute is device-sovereign: the device holds the authority and the
// controller is told. A muted device's microphone stays shut whatever the
// controller says.
func (d *Device) SendMuteState(muted bool) error {
	d.state.SetMuted(muted)
	return d.sendControl(protocol.MuteState{Type: protocol.TypeMuteState, Muted: muted})
}

// SendVolumeState reports a volume change, which the controller persists as
// the device's startup volume.
func (d *Device) SendVolumeState(level int) error {
	d.state.SetVolume(level)
	return d.sendControl(protocol.VolumeState{Type: protocol.TypeVolumeState, Level: level})
}

// SendAmbientLight reports a light reading.
func (d *Device) SendAmbientLight(lux int) error {
	return d.sendControl(protocol.AmbientLight{Type: protocol.TypeAmbientLight, Lux: lux})
}

// SendListenState reports what the device is doing with its microphone.
//
// The three states are the honest answers: the device scores its own wake
// word and sends nothing until it fires; the device streams continuously to
// the controller; or the device cannot run its own wake word and is
// button-only.
//
// This is the message a privacy statement rests on, so it is reported from
// what the device is doing rather than from what it was configured to do.
func (d *Device) SendListenState(state, reason string) error {
	d.state.SetListen(listenMode(state), reason)
	return d.sendControl(protocol.ListenState{Type: protocol.TypeListenState, State: state, Reason: reason})
}

// listenMode maps a reported listen state onto the SDK's own.
func listenMode(s string) state.ListenMode {
	switch s {
	case protocol.ListenLocal:
		return state.ListenLocal
	case protocol.ListenStream:
		return state.ListenStream
	case protocol.ListenDegraded:
		return state.ListenDegraded
	}
	return state.ListenUnknown
}

// SendOWWWake reports that the device's own wake word fired.
//
// The controller runs the voice turn from this. A device that scores its own
// wake word sends this instead of streaming audio all the time, which is
// what makes private listening possible.
//
// A non-zero session means the wake opened a private-listening session, and
// the audio follows as session-tagged frames rather than as the continuous
// stream.
func (d *Device) SendOWWWake(wake protocol.OWWWake) error {
	wake.Type = protocol.TypeOWWWake
	return d.sendControl(wake)
}

// StartListenSession opens a private-listening session with a cryptographically random session id.
// The caller should send the buffered pre-roll and subsequent samples with SendListenAudio, then stop on
// OnListenClose or a local timeout by calling EndListenSession.
func (d *Device) StartListenSession(wake protocol.OWWWake) (uint32, error) {
	if !d.HasCapability(protocol.CapOWWLocalOnly) {
		return 0, errors.New("echomuse: private listening requires the oww_local_only capability")
	}
	if !d.HasFeature(protocol.FeatureListenSession) {
		return 0, errors.New("echomuse: controller did not negotiate private-listening sessions")
	}
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, fmt.Errorf("echomuse: generating listen session id: %w", err)
	}
	session := binary.BigEndian.Uint32(b[:])
	if session == 0 {
		session = 1
	}
	d.mu.Lock()
	if d.listenAckTimer != nil {
		d.listenAckTimer.Stop()
	}
	if d.listenMaxTimer != nil {
		d.listenMaxTimer.Stop()
	}
	d.listenSession = session
	d.listenSeq = 0
	d.listenAckTimer = time.AfterFunc(listenAckTimeout, func() { _ = d.EndListenSession(session, "ack_timeout") })
	d.listenMaxTimer = time.AfterFunc(listenMaxOpen, func() { _ = d.EndListenSession(session, "max_open") })
	d.mu.Unlock()
	wake.Session = session
	if err := d.SendOWWWake(wake); err != nil {
		d.clearListenSession(session)
		return 0, err
	}
	return session, nil
}

// EndListenSession closes a private-listening session from the device side.
func (d *Device) EndListenSession(session uint32, reason string) error {
	if session == 0 {
		return fmt.Errorf("echomuse: private-listening session id must be non-zero")
	}
	if err := d.SendListenEnd(session, reason); err != nil {
		return err
	}
	d.clearListenSession(session)
	d.emitListenClose(protocol.ListenClose{Type: protocol.TypeListenClose, Session: session, Reason: reason})
	return nil
}

func (d *Device) clearListenSession(session uint32) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.listenSession != session {
		return
	}
	if d.listenAckTimer != nil {
		d.listenAckTimer.Stop()
		d.listenAckTimer = nil
	}
	if d.listenMaxTimer != nil {
		d.listenMaxTimer.Stop()
		d.listenMaxTimer = nil
	}
	d.listenSession, d.listenSeq = 0, 0
}

// SendOWWShadowCross reports a crossing in shadow mode.
//
// Shadow means the device scores and reports but does not act, so the
// controller's own detector and the device's can be compared on the same
// audio.
func (d *Device) SendOWWShadowCross(score float32, ageMs int64) error {
	return d.sendControl(protocol.OWWShadowCross{
		Type:  protocol.TypeOWWShadowCross,
		Score: score,
		AgeMs: ageMs,
	})
}

// SendListenEnd reports a session the device closed itself.
//
// The reason matters to the controller: it distinguishes a session that
// timed out from one the device was told to close.
func (d *Device) SendListenEnd(session uint32, reason string) error {
	err := d.sendControl(protocol.ListenEnd{
		Type:    protocol.TypeListenEnd,
		Session: session,
		Reason:  reason,
	})
	if err == nil {
		d.clearListenSession(session)
	}
	return err
}

// SendStats reports the periodic telemetry.
//
// Every field is optional and an absent one means "not measured". A device
// that reports zero for something it cannot measure reads as having
// measured zero, which is a different and wrong answer.
func (d *Device) SendStats(stats protocol.Stats) error {
	stats.Type = protocol.TypeStats
	return d.sendControl(stats)
}

// SendPlaybackStats reports the buffering result after one speaker stream drains.
func (d *Device) SendPlaybackStats(stats protocol.PlaybackStats) error {
	stats.Type = protocol.TypePlaybackStats
	return d.sendControl(stats)
}

// SendSendspinStatus reports the local player state to the controller.
func (d *Device) SendSendspinStatus(status any) error {
	return d.sendControl(protocol.SendspinStatus{Type: protocol.TypeSendspinStatus, Status: status})
}

// SendSendspinToken answers the controller's token request. Callers should fetch tokens just in time and must not log them.
func (d *Device) SendSendspinToken(token string) error {
	return d.SendSendspinTokenResponse(token, "", "")
}

// SendSendspinTokenResponse answers a token request with either a token/client id or an error reason.
func (d *Device) SendSendspinTokenResponse(token, clientID, reason string) error {
	if token == "" && reason == "" {
		return fmt.Errorf("echomuse: Sendspin token response needs a token or an error")
	}
	if token != "" && reason != "" {
		return fmt.Errorf("echomuse: Sendspin token response cannot contain both a token and an error")
	}
	return d.sendControl(protocol.SendspinToken{Type: protocol.TypeSendspinToken, Token: token, ClientID: clientID, Error: reason})
}

// SendLog writes a line into the device's log.
//
// The controller persists it and shows it on the dashboard, which is how a
// device says what it is doing to the person administering it.
func (d *Device) SendLog(level, message string) error {
	return d.sendControl(protocol.Log{
		Type:    protocol.TypeLog,
		Level:   level,
		Message: message,
	})
}

// sendControl writes one message on the control plane.
func (d *Device) sendControl(v any) error {
	d.mu.RLock()
	p := d.control
	d.mu.RUnlock()
	if p == nil {
		return ErrNotConnected
	}
	return p.writeJSON(v)
}

// SendMicAudio streams captured audio to the controller.
//
// The samples are signed 16-bit little-endian mono at 16 kHz. The sequence
// number increments on every frame, so a gap is visible to the controller as
// a gap rather than as silence.
//
// The frame is not copied, so the caller must not reuse the buffer until
// the call returns. It is written to the socket synchronously.
func (d *Device) SendMicAudio(pcm []byte) error {
	d.mu.RLock()
	p := d.data
	d.mu.RUnlock()
	if p == nil {
		return ErrNotConnected
	}

	d.mu.Lock()
	d.micSeq++
	seq := d.micSeq
	d.mu.Unlock()

	return p.writeBinary(protocol.MicFrame{Seq: seq, PCM: pcm}.Encode())
}

// SendListenAudio streams private-listening audio to the controller.
//
// The samples are signed 16-bit little-endian mono at 16 kHz, numbered
// within their session.
func (d *Device) SendListenAudio(session uint32, pcm []byte) error {
	if session == 0 {
		return fmt.Errorf("echomuse: private-listening session id must be non-zero")
	}
	if len(pcm) == 0 || len(pcm)%2 != 0 {
		return fmt.Errorf("echomuse: private-listening audio must contain whole 16-bit samples")
	}
	d.mu.RLock()
	p := d.data
	d.mu.RUnlock()
	if p == nil {
		return ErrNotConnected
	}

	d.mu.Lock()
	if d.listenSession != 0 && d.listenSession != session {
		d.mu.Unlock()
		return fmt.Errorf("echomuse: session %d is not the active private-listening session", session)
	}
	if d.listenSession != session {
		d.listenSession = session
		d.listenSeq = 0
	}
	seq := d.listenSeq
	d.listenSeq++
	d.mu.Unlock()

	return p.writeBinary(protocol.ListenFrame{
		Session: session,
		Seq:     seq,
		PCM:     pcm,
	}.Encode())
}

// SendVADEnd ends a bounded turn: speech was detected, and then ended.
//
// It is distinct from the no-speech sentinel because "the wake word then
// silence" has to be tellable from "spoke, and the backend had nothing to
// say".
func (d *Device) SendVADEnd() error {
	return d.sendBinary(protocol.EncodeVADEnd())
}

// SendNoSpeechTimeout ends a bounded turn in which no speech was ever
// detected.
func (d *Device) SendNoSpeechTimeout() error {
	return d.sendBinary(protocol.EncodeNoSpeechTimeout())
}

// sendBinary writes one frame on the data plane.
func (d *Device) sendBinary(b []byte) error {
	d.mu.RLock()
	p := d.data
	d.mu.RUnlock()
	if p == nil {
		return ErrNotConnected
	}
	return p.writeBinary(b)
}
