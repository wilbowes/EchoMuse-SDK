package echomuse

import (
	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/protocol"
)

// The callbacks below are what the controller asks of a device. An
// application registers the ones its hardware can act on and ignores the
// rest — a device with no ring registers nothing for the ring, and a device
// with no speaker registers nothing for playback.
//
// Every callback is invoked on the goroutine that read the message, so a
// callback that blocks holds the link up. Hand work to another goroutine
// rather than doing it here.

// OnConfig registers a handler for the controller's configuration push.
//
// The push is a whole message, not a patch: a key that is absent was not
// sent. A device that does not understand a key ignores it, which is the
// correct degrade.
func (d *Device) OnConfig(fn func(protocol.Config)) {
	d.callbacksMu.Lock()
	d.onConfig = append(d.onConfig, fn)
	d.callbacksMu.Unlock()
}

// OnLEDs registers a handler for an LED frame.
//
// The controller renders every animation frame and the device paints it, so
// a device with a ring shows exactly what an Echo shows.
func (d *Device) OnLEDs(fn func(protocol.LEDs)) {
	d.callbacksMu.Lock()
	d.onLEDs = append(d.onLEDs, fn)
	d.callbacksMu.Unlock()
}

// OnLEDAnim registers a handler for an animation spec.
//
// The device renders the animation itself, on its own ticker, until a newer
// spec arrives or the spec's dead-man TTL expires.
func (d *Device) OnLEDAnim(fn func(protocol.AnimSpec)) {
	d.callbacksMu.Lock()
	d.onLEDAnim = append(d.onLEDAnim, fn)
	d.callbacksMu.Unlock()
}

// OnMicStart registers a handler for the controller asking for microphone
// audio.
//
// The argument is whether the stream is a bounded turn. Without it the
// stream is the always-on wake stream, ungated and continuous. With it the
// stream is gated on speech and the device ends it with a sentinel.
func (d *Device) OnMicStart(fn func(lockMic bool)) {
	d.callbacksMu.Lock()
	d.onMicStart = append(d.onMicStart, fn)
	d.callbacksMu.Unlock()
}

// OnMicStop registers a handler for the controller stopping the microphone.
func (d *Device) OnMicStop(fn func()) {
	d.callbacksMu.Lock()
	d.onMicStop = append(d.onMicStop, fn)
	d.callbacksMu.Unlock()
}

// OnSpeaker registers a handler for playback audio.
//
// The samples are signed 16-bit little-endian mono at 48 kHz — a different
// rate from the capture side, which is 16 kHz.
func (d *Device) OnSpeaker(fn func(pcm []byte)) {
	d.callbacksMu.Lock()
	d.onSpeaker = append(d.onSpeaker, fn)
	d.callbacksMu.Unlock()
}

// OnSpeakerEOS registers a handler for the end of a response.
func (d *Device) OnSpeakerEOS(fn func()) {
	d.callbacksMu.Lock()
	d.onSpeakerEOS = append(d.onSpeakerEOS, fn)
	d.callbacksMu.Unlock()
}

// OnMusic registers a handler for music audio.
//
// Music arrives on its own frame type and is only sent to a device that
// announced the `audio_mix` capability, because only such a device can mix
// it under the voice rather than pausing it.
func (d *Device) OnMusic(fn func(pcm []byte)) {
	d.callbacksMu.Lock()
	d.onMusic = append(d.onMusic, fn)
	d.callbacksMu.Unlock()
}

// OnMusicEOS registers a handler for the end of a music stream.
func (d *Device) OnMusicEOS(fn func()) {
	d.callbacksMu.Lock()
	d.onMusicEOS = append(d.onMusicEOS, fn)
	d.callbacksMu.Unlock()
}

// OnCue registers a handler for a sound the device generates itself.
//
// The only cue today is the wake confirmation, and it is sent when the wake
// sound is on and a wake has won arbitration. An unknown cue name is
// ignored by the controller and never arrives.
func (d *Device) OnCue(fn func(cue string)) {
	d.callbacksMu.Lock()
	d.onCue = append(d.onCue, fn)
	d.callbacksMu.Unlock()
}

// OnVolumeSet registers a handler for a volume change from Home Assistant.
func (d *Device) OnVolumeSet(fn func(level int)) {
	d.callbacksMu.Lock()
	d.onVolumeSet = append(d.onVolumeSet, fn)
	d.callbacksMu.Unlock()
}

// OnDuck registers a handler for the duck request that mixes music under a
// voice turn.
func (d *Device) OnDuck(fn func(on bool)) {
	d.callbacksMu.Lock()
	d.onDuck = append(d.onDuck, fn)
	d.callbacksMu.Unlock()
}

// OnFlush registers a handler for a buffer flush.
//
// The kind is "speaker" or "music". A flush is how a barge-in or a
// cancellation stops audio that has already been sent but not yet played.
func (d *Device) OnFlush(fn func(kind string)) {
	d.callbacksMu.Lock()
	d.onFlush = append(d.onFlush, fn)
	d.callbacksMu.Unlock()
}

// OnListenAck registers a callback for the controller accepting a private-listening wake.
func (d *Device) OnListenAck(fn func(session uint32)) {
	d.callbacksMu.Lock()
	d.onListenAck = append(d.onListenAck, fn)
	d.callbacksMu.Unlock()
}

// OnListenClose registers a callback for the controller ending a private-listening session.
func (d *Device) OnListenClose(fn func(protocol.ListenClose)) {
	d.callbacksMu.Lock()
	d.onListenClose = append(d.onListenClose, fn)
	d.callbacksMu.Unlock()
}

// OnSendspinTokenRequest registers a callback that should obtain and answer with a current player token.
func (d *Device) OnSendspinTokenRequest(fn func()) {
	d.callbacksMu.Lock()
	d.onSendspinTokenRequest = append(d.onSendspinTokenRequest, fn)
	d.callbacksMu.Unlock()
}

func (d *Device) emitListenAck(session uint32) {
	d.callbacksMu.RLock()
	fns := append([]func(uint32){}, d.onListenAck...)
	d.callbacksMu.RUnlock()
	for _, fn := range fns {
		fn(session)
	}
}

func (d *Device) emitListenClose(m protocol.ListenClose) {
	d.callbacksMu.RLock()
	fns := append([]func(protocol.ListenClose){}, d.onListenClose...)
	d.callbacksMu.RUnlock()
	for _, fn := range fns {
		fn(m)
	}
}

func (d *Device) emitSendspinTokenRequest() {
	d.callbacksMu.RLock()
	fns := append([]func(){}, d.onSendspinTokenRequest...)
	d.callbacksMu.RUnlock()
	for _, fn := range fns {
		fn()
	}
}

// OnRaw registers a handler for every control-plane message.
//
// It is the escape hatch for a message the SDK does not model, so an
// application can react to something new without waiting for the SDK to
// learn it.
func (d *Device) OnRaw(fn func(protocol.Incoming)) {
	d.callbacksMu.Lock()
	d.onRaw = append(d.onRaw, fn)
	d.callbacksMu.Unlock()
}

// OnLog registers a handler for log lines the application writes.
//
// The lines are persisted by the controller and shown on the dashboard, so
// they are the way an application says what it is doing.
func (d *Device) OnLog(fn func(level, message string)) {
	d.callbacksMu.Lock()
	d.onLog = append(d.onLog, fn)
	d.callbacksMu.Unlock()
}

// emitConfig notifies the config handlers.
func (d *Device) emitConfig(c protocol.Config) {
	d.callbacksMu.RLock()
	fns := append([]func(protocol.Config){}, d.onConfig...)
	d.callbacksMu.RUnlock()
	for _, fn := range fns {
		fn(c)
	}
}

// emitLEDs notifies the LED frame handlers.
func (d *Device) emitLEDs(m protocol.LEDs) {
	d.callbacksMu.RLock()
	fns := append([]func(protocol.LEDs){}, d.onLEDs...)
	d.callbacksMu.RUnlock()
	for _, fn := range fns {
		fn(m)
	}
}

// emitLEDAnim notifies the animation handlers.
func (d *Device) emitLEDAnim(a protocol.AnimSpec) {
	d.callbacksMu.RLock()
	fns := append([]func(protocol.AnimSpec){}, d.onLEDAnim...)
	d.callbacksMu.RUnlock()
	for _, fn := range fns {
		fn(a)
	}
}

// emitMicStart notifies the microphone handlers.
func (d *Device) emitMicStart(lockMic bool) {
	d.callbacksMu.RLock()
	fns := append([]func(bool){}, d.onMicStart...)
	d.callbacksMu.RUnlock()
	for _, fn := range fns {
		fn(lockMic)
	}
}

// emitMicStop notifies the microphone handlers.
func (d *Device) emitMicStop() {
	d.callbacksMu.RLock()
	fns := append([]func(){}, d.onMicStop...)
	d.callbacksMu.RUnlock()
	for _, fn := range fns {
		fn()
	}
}

// emitSpeaker notifies the playback handlers.
func (d *Device) emitSpeaker(pcm []byte) {
	d.callbacksMu.RLock()
	fns := append([]func([]byte){}, d.onSpeaker...)
	d.callbacksMu.RUnlock()
	for _, fn := range fns {
		fn(pcm)
	}
}

// emitSpeakerEOS notifies the playback handlers.
func (d *Device) emitSpeakerEOS() {
	d.callbacksMu.RLock()
	fns := append([]func(){}, d.onSpeakerEOS...)
	d.callbacksMu.RUnlock()
	for _, fn := range fns {
		fn()
	}
}

// emitMusic notifies the music handlers.
func (d *Device) emitMusic(pcm []byte) {
	d.callbacksMu.RLock()
	fns := append([]func([]byte){}, d.onMusic...)
	d.callbacksMu.RUnlock()
	for _, fn := range fns {
		fn(pcm)
	}
}

// emitMusicEOS notifies the music handlers.
func (d *Device) emitMusicEOS() {
	d.callbacksMu.RLock()
	fns := append([]func(){}, d.onMusicEOS...)
	d.callbacksMu.RUnlock()
	for _, fn := range fns {
		fn()
	}
}

// emitCue notifies the cue handlers.
func (d *Device) emitCue(cue string) {
	d.callbacksMu.RLock()
	fns := append([]func(string){}, d.onCue...)
	d.callbacksMu.RUnlock()
	for _, fn := range fns {
		fn(cue)
	}
}

// emitVolumeSet notifies the volume handlers.
func (d *Device) emitVolumeSet(level int) {
	d.callbacksMu.RLock()
	fns := append([]func(int){}, d.onVolumeSet...)
	d.callbacksMu.RUnlock()
	for _, fn := range fns {
		fn(level)
	}
}

// emitDuck notifies the duck handlers.
func (d *Device) emitDuck(on bool) {
	d.callbacksMu.RLock()
	fns := append([]func(bool){}, d.onDuck...)
	d.callbacksMu.RUnlock()
	for _, fn := range fns {
		fn(on)
	}
}

// emitFlush notifies the flush handlers.
func (d *Device) emitFlush(kind string) {
	d.callbacksMu.RLock()
	fns := append([]func(string){}, d.onFlush...)
	d.callbacksMu.RUnlock()
	for _, fn := range fns {
		fn(kind)
	}
}

// emitRaw notifies the raw handlers.
func (d *Device) emitRaw(m protocol.Incoming) {
	d.callbacksMu.RLock()
	fns := append([]func(protocol.Incoming){}, d.onRaw...)
	d.callbacksMu.RUnlock()
	for _, fn := range fns {
		fn(m)
	}
}
