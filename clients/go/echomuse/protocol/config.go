package protocol

import (
	"encoding/json"
	"fmt"
)

// Config is the configuration the controller pushes to a device.
//
// The push is a whole message, not a patch: a key that is absent was not
// sent, and the device keeps whatever it had. Several keys are pointers,
// because for those the difference between "off" and "not sent" is the
// difference between a setting the user chose and a setting nobody touched.
//
// A device that does not understand a key ignores it, which is the correct
// degrade — so a device built on this struct simply does not read the keys
// its hardware has no use for.
//
// This is a view of the controller's config, not a copy of the Echo's. The
// capture-chain keys (the microphone's analog gains, the echo-cancellation
// tuning) describe the Echo Dot's own hardware and mean nothing on a device
// with a different signal path. They are present so a push decodes cleanly,
// and are documented as such.
type Config struct {
	Type string `json:"type,omitempty"`

	// ── Microphone capture ───────────────────────────────────────────────
	// These describe the Echo Dot's analog capture path. A device with a
	// different microphone front-end has no use for them, and applies
	// whatever gain its own hardware needs.
	AdcDigitalGain       *int     `json:"adcDigitalGain,omitempty"`
	AdcMicpga            *int     `json:"adcMicpga,omitempty"`
	MicGainDb            *int     `json:"micGainDb,omitempty"`
	VadThreshold         float64  `json:"vadThreshold,omitempty"`
	VadSpeechMs          int      `json:"vadSpeechMs,omitempty"`
	VadSilenceMs         int      `json:"vadSilenceMs,omitempty"`
	BeamAngle            *float64 `json:"beamAngle,omitempty"`
	BeamformingEnabled   *bool    `json:"beamformingEnabled,omitempty"`
	HasBeamforming       bool     `json:"hasBeamforming,omitempty"`
	AgcEnabled           *bool    `json:"agcEnabled,omitempty"`
	AecEnabled           *bool    `json:"aecEnabled,omitempty"`
	AecDelayMs           *int     `json:"aecDelayMs,omitempty"`
	AecTailMs            int      `json:"aecTailMs,omitempty"`
	AecRefSource         string   `json:"aecRefSource,omitempty"`
	WakeMic              *int     `json:"wakeMic,omitempty"`
	ButtonSingleTapEvent *bool    `json:"buttonSingleTapEvent,omitempty"`
	ButtonMultiTapMs     *int     `json:"buttonMultiTapMs,omitempty"`
	WakeArbitrationMs    *int     `json:"wakeArbitrationMs,omitempty"`

	// ── Wake word ────────────────────────────────────────────────────────
	// OwwModel is the model name. For a built-in model it is a bare name;
	// for a custom one it is a file path ending in ".onnx".
	OwwModel     string  `json:"owwModel,omitempty"`
	OwwOnDevice  string  `json:"owwOnDevice,omitempty"`
	OwwThreshold float64 `json:"owwThreshold,omitempty"`
	// WakeWordEnabled is a pointer: false is "No wake word", and that is
	// the value that matters.
	WakeWordEnabled  *bool    `json:"wakeWordEnabled,omitempty"`
	BargeInEnabled   *bool    `json:"bargeInEnabled,omitempty"`
	BargeInThreshold float64  `json:"bargeInThreshold,omitempty"`
	WakeSound        *bool    `json:"wakeSound,omitempty"`
	WakeSoundLevel   string   `json:"wakeSoundLevel,omitempty"`
	StreamReply      *bool    `json:"streamReply,omitempty"`
	OWWSpeexNS       *bool    `json:"owwSpeexNs,omitempty"`
	NSASR            *bool    `json:"nsAsr,omitempty"`
	SaveUtterances   *bool    `json:"saveUtterances,omitempty"`
	WakeClipCapture  *bool    `json:"wakeClipCapture,omitempty"`
	WakeClipMinScore *float64 `json:"wakeClipMinScore,omitempty"`

	// ── Playback ─────────────────────────────────────────────────────────
	StartupVolume     *int     `json:"startupVolume,omitempty"`
	DuckDb            *float64 `json:"duckDb,omitempty"`
	ResponseLevel     string   `json:"responseLevel,omitempty"`
	VolumeButtonSound *bool    `json:"volumeButtonSound,omitempty"`
	RemoteVolumeArc   *bool    `json:"remoteVolumeArc,omitempty"`

	// The output chain: the equaliser, the bass guard and the limiter.
	// Applied by the controller before the audio reaches the wire, unless
	// the device announced `output_chain` and the controller announced it
	// too — then both halves would shape the same audio.
	EqBands          []float64 `json:"eqBands,omitempty"`
	EqLoudness       *bool     `json:"eqLoudness,omitempty"`
	BassGuardEnabled *bool     `json:"bassGuardEnabled,omitempty"`
	BassGuardDb      *float64  `json:"bassGuardDb,omitempty"`
	LimiterEnabled   *bool     `json:"limiterEnabled,omitempty"`
	LimiterThreshold *float64  `json:"limiterThreshold,omitempty"`
	LimiterRelease   *float64  `json:"limiterRelease,omitempty"`

	// ── Ring ─────────────────────────────────────────────────────────────
	// The ring colours are carried as a scene name and two colours rather
	// than a frame, because the controller renders the animation and the
	// device paints the frames.
	LedScene       string `json:"ledScene,omitempty"`
	LedListenColor string `json:"ledListenColor,omitempty"`
	LedThinkColor  string `json:"ledThinkColor,omitempty"`

	// ── Bluetooth and multi-room ─────────────────────────────────────────
	BleProxyEnabled     *bool   `json:"bleProxyEnabled,omitempty"`
	BleProxyConnections *bool   `json:"bleProxyConnections,omitempty"`
	SendspinEnabled     *bool   `json:"sendspinEnabled,omitempty"`
	SendspinUnpaired    *bool   `json:"sendspinUnpaired,omitempty"`
	SendspinName        *string `json:"sendspinName,omitempty"`

	// ── Shell console ────────────────────────────────────────────────────
	// Both pointers: an empty password is the legitimate "no password"
	// setting, and a zero timeout is the legitimate "no timeout". With a
	// plain value and omitempty, clearing either would be indistinguishable
	// from a field nobody sent.
	ConsolePassword   *string `json:"consolePassword,omitempty"`
	ConsoleTimeoutMin *int    `json:"consoleTimeoutMin,omitempty"`
}

// Wake word modes. The stored values are the three the controller knows, and
// the dashboard does not offer `shadow`: it is a developer diagnostic that
// still exists for comparing the two detectors on the same audio.
const (
	// WakeOnDevice — the device scores its own wake word and sends
	// nothing until it fires.
	WakeOnDevice = "on"
	// WakeOnController — the controller scores and triggers, which means
	// audio streams to it continuously.
	WakeOnController = "off"
	// WakeShadow — both detectors score a continuous stream and neither
	// acts. A diagnostic, not a user choice.
	WakeShadow = "shadow"
)

// DecodeConfig reads a config push.
func DecodeConfig(raw json.RawMessage) (Config, error) {
	var c Config
	if err := json.Unmarshal(raw, &c); err != nil {
		return Config{}, fmt.Errorf("echomuse: decoding config: %w", err)
	}
	return c, nil
}
