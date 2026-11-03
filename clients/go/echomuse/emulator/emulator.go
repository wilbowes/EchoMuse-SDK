package emulator

import (
	"context"
	"fmt"

	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse"
	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/display"
	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/protocol"
	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/sendspin"
	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/state"
	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/wakeword"
)

// Options configures an emulator.
type Options struct {
	// ControllerURL is the controller to join. Empty finds it by mDNS.
	ControllerURL string

	// ControllerTLSPort is the controller's encrypted port. Zero means the
	// controller offers none, and the device dials plain.
	ControllerTLSPort int
	// DeviceID is the identity the controller knows this device by. Empty
	// derives one from the machine's hostname.
	DeviceID string
	// Label is the name the device appears under.
	Label string
	// CredentialsDir is where the link credential is kept.
	CredentialsDir string
	// Audio is how the emulator reaches the machine's sound card.
	Audio AudioBackend
	// SendspinOutput provides clocked stereo output for synchronized music.
	// If nil, a platform backend is selected when available; otherwise Audio is used.
	// Log receives the emulator's diagnostics.
	Log *slog.Logger
	// MuteCapture stops the microphone reaching the controller, for a
	// demonstration where the room should not be heard.
	MuteCapture bool
	// LocalWake enables device-local wakeword scoring with a locally loaded inference backend.
	LocalWake      *LocalWakeOptions
	SendspinOutput SendspinOutputBackend
}

// Emulator is a device that behaves as the controller expects.
type Emulator struct {
	opts Options
	log  *slog.Logger

	device *echomuse.Device
	ring   *display.Tracker

	mu                  sync.Mutex
	audio               AudioBackend
	sendspinOutput      SendspinOutputBackend
	sendspinOut         ClockedSendspinOutput
	speaker             *speakerHandle
	capture             *captureHandle
	captureCancel       context.CancelFunc
	capturing           bool
	captureGen          int
	level               int
	volume              int
	muted               bool
	localWake           *LocalWakeOptions
	wakeListener        *wakeword.Listener
	wakeMode            string
	wakeSession         uint32
	turnStream          bool
	sendspin            *sendspin.Client
	sendspinName        string
	sendspinUnpaired    bool
	sendspinMuted       bool
	voiceActive         bool
	voiceMixed          bool
	lastSendspinAttempt time.Time
	lastClockedAttempt  time.Time
	duckDB              float64
	musicDucked         bool
	voiceMixMu          sync.Mutex
	voiceMixCond        *sync.Cond
	voiceMix            []byte
	phase               string
	phaseMsg            string
	steps               []Step
	listeners           []func()

	// ctx bounds the audio streams. It is set by Run, because the
	// callbacks that open the sound card are registered before the
	// context exists.
	ctx context.Context
}

// LocalWakeOptions supplies the loaded local model. The caller owns Inferer and must Close it after Run exits.
type LocalWakeOptions struct {
	Inferer   wakeword.Inferer
	ModelName string
	Threshold float32
}

// Step is one line of the walkthrough.
//
// The emulator shows its progress as a list of steps, because the pairing
// flow involves a person on the dashboard and a person at the keyboard, and
// both need to see where the other got to.
type Step struct {
	// Name is what the step is.
	Name string `json:"name"`
	// Done is whether it has completed.
	Done bool `json:"done"`
	// Detail says what happened.
	Detail string `json:"detail"`
}

// New returns an emulator.
func New(opts Options) (*Emulator, error) {
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	if opts.DeviceID == "" {
		opts.DeviceID = defaultDeviceID()
	}
	if opts.CredentialsDir == "" {
		opts.CredentialsDir = filepath.Join(userHome(), ".echomuse", "emulator")
	}
	if opts.Audio == nil {
		return nil, fmt.Errorf("emulator: Audio backend is required; provide a system-specific implementation")
	}
	if opts.SendspinOutput == nil {
		if clocked, ok := opts.Audio.(SendspinOutputBackend); ok {
			opts.SendspinOutput = clocked
		}
	}
	if opts.LocalWake != nil {
		if opts.LocalWake.Inferer == nil {
			return nil, fmt.Errorf("emulator: LocalWake requires an inference backend")
		}
		if opts.LocalWake.ModelName == "" {
			return nil, fmt.Errorf("emulator: LocalWake requires the model name expected from controller config")
		}
	}

	e := &Emulator{
		opts:           opts,
		log:            opts.Log,
		ring:           display.NewTracker(),
		audio:          opts.Audio,
		sendspinOutput: opts.SendspinOutput,
		volume:         50,
		duckDB:         -18,
		localWake:      opts.LocalWake,
	}
	e.voiceMixCond = sync.NewCond(&e.voiceMixMu)
	if opts.LocalWake != nil {
		threshold := opts.LocalWake.Threshold
		if threshold <= 0 {
			threshold = 0.5
		}
		listener, err := wakeword.NewListener(opts.LocalWake.Inferer, threshold, false, e.onWakeCrossing)
		if err != nil {
			return nil, err
		}
		e.wakeListener = listener
	}

	device, err := echomuse.New(echomuse.Options{
		DeviceID:          opts.DeviceID,
		Version:           "0.1.0",
		Board:             "emulator",
		Capabilities:      emulatorCapabilities(opts.LocalWake != nil),
		ControllerURL:     opts.ControllerURL,
		ControllerTLSPort: opts.ControllerTLSPort,
		CredentialsDir:    opts.CredentialsDir,
		Logger:            opts.Log,
	})
	if err != nil {
		return nil, err
	}
	e.device = device

	device.State().OnChange(func(s state.Snapshot) {
		e.onState(s)
	})
	device.OnLEDs(e.ring.OnLEDs)
	device.OnLEDAnim(e.ring.OnLEDAnim)
	device.OnSpeaker(func(pcm []byte) {
		e.setVoiceActive(true)
		e.StartPlayback(e.ctx)
		e.play(pcm)
	})
	device.OnSpeakerEOS(func() {
		e.log.Debug("the response finished")
		e.finishSpeakerResponse()
		e.setVoiceActive(false)
	})
	device.OnDuck(func(on bool) {
		e.mu.Lock()
		e.musicDucked = on
		duckDB := e.duckDB
		e.mu.Unlock()
		e.log.Info("Sendspin music duck state changed", "on", on, "duck_db", duckDB)
	})
	device.OnFlush(func(kind string) {
		if kind != "speaker" {
			return
		}
		e.clearVoiceMix()
		e.setVoiceActive(false)
		e.mu.Lock()
		hasMusic := e.sendspin != nil && e.sendspin.Active()
		e.mu.Unlock()
		if !hasMusic {
			e.StopPlayback()
		}
	})
	device.OnMicStart(func(lockMic bool) {
		e.mu.Lock()
		muted := e.muted
		local := e.wakeMode != ""
		if local && !muted {
			e.turnStream = true
		}
		e.mu.Unlock()
		if muted {
			e.log.Info("the microphone start was ignored because mute is enabled")
			return
		}
		e.StartCapture(e.ctx)
		e.ReportListenState()
	})
	device.OnMicStop(func() {
		e.mu.Lock()
		local := e.wakeMode != ""
		e.turnStream = false
		e.mu.Unlock()
		if !local {
			e.StopCapture()
		}
		e.ReportListenState()
	})
	device.OnConfig(func(c protocol.Config) {
		e.log.Info("the controller pushed a configuration", "model", c.OwwModel)
		if c.StartupVolume != nil {
			e.setVolume(*c.StartupVolume)
		}
		if c.DuckDb != nil {
			e.mu.Lock()
			e.duckDB = *c.DuckDb
			e.mu.Unlock()
		}
		e.applyWakeConfig(c)
		e.applySendspinConfig(c)
	})
	device.OnVolumeSet(func(level int) { e.setVolume(level) })
	device.OnListenAck(func(session uint32) { e.log.Info("private wake accepted", "session", session) })
	device.OnListenClose(func(m protocol.ListenClose) { e.localListenClosed(m) })
	device.OnSendspinTokenRequest(func() { e.sendSendspinToken() })
	device.OnLog(func(level, message string) {
		e.log.Info("the device said", "level", level, "message", message)
	})

	return e, nil
}

// emulatorCapabilities are what this device can do.
//
// It announces the pairing capability, because asking to be admitted is
// how a device that the controller has never met gets in — and the
// approval is what issues the link credential.
//
// It does not announce `leds` or `led_anim`: this device has a window, not
// a ring, and it draws the state rather than the pixels.
func emulatorCapabilities(localWake bool) []protocol.Capability {
	caps := []protocol.Capability{
		protocol.CapMic,
		protocol.CapSpeaker,
		protocol.CapButtons,
		protocol.CapButtonHold,
		protocol.CapPairing,
		protocol.CapSendspin,
	}
	if localWake {
		caps = append(caps, protocol.CapOWWLocalOnly, protocol.CapOWWTrigger)
	}
	return caps
}

// Device returns the underlying device, for an application that wants to
// drive it directly.
func (e *Emulator) Device() *echomuse.Device { return e.device }

// AudioDescription says what the audio backend is.
func (e *Emulator) AudioDescription() string { return e.opts.Audio.Describe() }

// SetVolume changes playback gain and reports the new level to the controller.
func (e *Emulator) SetVolume(level int) error {
	e.setVolume(level)
	return e.device.SendVolumeState(level)
}

func (e *Emulator) setVolume(level int) {
	if level < 0 {
		level = 0
	}
	if level > 100 {
		level = 100
	}
	e.mu.Lock()
	e.volume = level
	player := e.sendspin
	e.mu.Unlock()
	if player != nil {
		player.SetVolume(level)
	}
}

func applyVolume(pcm []byte, level int) []byte {
	out := append([]byte(nil), pcm...)
	for i := 0; i+1 < len(out); i += 2 {
		s := int16(uint16(out[i]) | uint16(out[i+1])<<8)
		s = int16(int32(s) * int32(level) / 100)
		out[i], out[i+1] = byte(s), byte(uint16(s)>>8)
	}
	return out
}

// SetMuted applies a local microphone mute and reports the device-owned state.
func (e *Emulator) SetMuted(muted bool) error {
	e.mu.Lock()
	e.muted = muted
	ctx := e.ctx
	wakeMode := e.wakeMode
	e.mu.Unlock()
	if muted {
		e.StopCapture()
	} else if ctx != nil && (wakeMode != "" || e.device.State().Snapshot().Listening) {
		e.StartCapture(ctx)
	}
	return e.device.SendMuteState(muted)
}

// SelectAudioDevices asks the injected backend to switch voice endpoints.
func (e *Emulator) SelectAudioDevices(input, output string) error {
	backend, ok := e.audio.(AudioDeviceProvider)
	if !ok {
		return fmt.Errorf("audio backend does not support device selection")
	}
	e.mu.Lock()
	wasCapturing := e.capturing
	ctx := e.ctx
	e.mu.Unlock()
	if err := backend.SelectAudioDevices(input, output); err != nil {
		return err
	}
	e.closeSendspinOutput()
	e.StopCapture()
	e.StopPlayback()
	if wasCapturing && ctx != nil {
		e.StartCapture(ctx)
	}
	return nil
}

// Ring returns the ring tracker, for a window that draws the ring.
func (e *Emulator) Ring() *display.Tracker { return e.ring }

// Run joins the controller and stays joined.
//
// It returns when the context is cancelled.
func (e *Emulator) Run(ctx context.Context) error {
	e.mu.Lock()
	e.ctx = ctx
	e.mu.Unlock()

	e.setStep("Find the controller", e.opts.ControllerURL != "")
	e.setPhase("Looking for a controller", "")
	e.startSendspinPump(ctx)

	// The pairing window opens before the first dial, so the very first
	// registration already carries the request. A device that asks after
	// it has been seen as pending still gets admitted, but the request
	// arrives later and the administrator sees an ordinary pending
	// device rather than a pairing one.
	//
	// A device that already holds a link credential does not ask. It has
	// been admitted, and asking again leaves the dashboard showing an
	// approval for a device that is already in the fleet.
	if !e.device.Paired() {
		e.device.StartPairing()
	}
	e.setStep("Ask to be admitted", false)
	defer func() {
		e.stopSendspin("restart")
		e.StopCapture()
		e.StopPlayback()
	}()
	return e.device.Run(ctx)
}

// onState reacts to the device's state changing.
func (e *Emulator) onState(s state.Snapshot) {
	e.mu.Lock()
	defer e.mu.Unlock()

	switch s.Phase {
	case state.PhaseReady:
		e.setStepLocked("Ask to be admitted", true, "the controller admitted this device")
		e.setStepLocked("Get the link credential", e.credentialSeen(), "the controller issued it over the shell plane")
		e.setPhaseLocked("Ready", "joined the controller")
	case state.PhasePending:
		e.setStepLocked("Ask to be admitted", false, "waiting for an administrator to approve it")
		e.setPhaseLocked("Waiting for approval", s.PhaseReason)
	case state.PhaseRefused:
		e.setStepLocked("Ask to be admitted", false, "the controller refused this device")
		e.setPhaseLocked("Refused", s.PhaseReason)
	case state.PhaseDisconnected:
		e.setPhaseLocked("Disconnected", s.PhaseReason)
	}
}

// credentialSeen reports whether the link credential has arrived.
func (e *Emulator) credentialSeen() bool {
	token := filepath.Join(e.opts.CredentialsDir, "token")
	if _, err := os.Stat(token); err != nil {
		return false
	}
	return true
}

// Steps returns the walkthrough's progress.
func (e *Emulator) Steps() []Step {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]Step(nil), e.steps...)
}

// setStep records a step's progress.
func (e *Emulator) setStep(name string, done bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.setStepLocked(name, done, "")
}

// setStepLocked records a step's progress, with the lock held.
func (e *Emulator) setStepLocked(name string, done bool, detail string) {
	for i := range e.steps {
		if e.steps[i].Name == name {
			e.steps[i].Done = done
			if detail != "" {
				e.steps[i].Detail = detail
			}
			e.notifyLocked()
			return
		}
	}
	e.steps = append(e.steps, Step{Name: name, Done: done, Detail: detail})
	e.notifyLocked()
}

// setPhase records what the emulator is doing.
func (e *Emulator) setPhase(phase, msg string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.setPhaseLocked(phase, msg)
}

// setPhaseLocked records what the emulator is doing, with the lock held.
func (e *Emulator) setPhaseLocked(phase, msg string) {
	e.phase = phase
	e.phaseMsg = msg
	e.notifyLocked()
}

// Phase returns what the emulator is doing.
func (e *Emulator) Phase() (string, string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.phase, e.phaseMsg
}

// OnChange registers a listener for the emulator's state.
func (e *Emulator) OnChange(fn func()) {
	e.mu.Lock()
	e.listeners = append(e.listeners, fn)
	e.mu.Unlock()
}

// notifyLocked tells the listeners, with the lock held.
func (e *Emulator) notifyLocked() {
	for _, fn := range e.listeners {
		fn()
	}
}

// play sends the controller's audio to the speaker.
//
// The playback stream is opened on the first chunk and kept open, because
// opening a sound card per response costs more than the response does.
//
// A failed write closes the stream. The sound card has already refused
// the audio, and every later chunk would fail the same way — the
// controller's whole response would arrive as one warning per chunk, and
// the reason would be in none of them.
func (e *Emulator) play(pcm []byte) {
	e.mu.Lock()
	speaker := e.speaker
	volume := e.volume
	e.mu.Unlock()

	if volume < 100 {
		pcm = applyVolume(pcm, volume)
	}
	e.mu.Lock()
	mixWithMusic := e.voiceActive && e.voiceMixed
	e.mu.Unlock()
	if mixWithMusic {
		e.queueVoiceMix(pcm)
		return
	}
	if speaker == nil {
		return
	}
	if _, err := speaker.Write(pcm); err != nil {
		e.log.Warn("the speaker did not take the audio", "error", err)
		e.StopPlayback()
		e.setPhase("Speaker unavailable", e.speakerReason())
	}
}

// speakerReason says why the speaker failed, as the audio backend knows
// it.
//
// The backend reports ALSA's device error, which identifies the cause more clearly than the stream write error.
func (e *Emulator) speakerReason() string {
	reporter, ok := e.audio.(FailureReporter)
	if !ok {
		return "the speaker refused the audio"
	}
	if reason := reporter.LastError(); reason != "" {
		return reason
	}
	return "the speaker refused the audio"
}

// speaker is the machine's speaker.
type speakerHandle struct {
	mu sync.Mutex
	w  io.WriteCloser
}

// Write plays samples.
func (s *speakerHandle) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// Close stops the speaker.
func (s *speakerHandle) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Close()
}

// defaultDeviceID derives an identifier from the machine's hostname.
func defaultDeviceID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		return "echomuse-emulator"
	}
	return "emulator-" + host
}

// userHome is the user's home directory.
func userHome() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return home
}
