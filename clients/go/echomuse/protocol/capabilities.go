// Package protocol holds the wire types of the EchoMuse device↔controller
// interface: the capability strings, the control-plane message shapes and the
// binary frame codes of the data plane.
//
// Everything here is a faithful restatement of the controller's side of the
// contract (docs/device-controller-interface.md in the EchoMuse repository).
// The SDK does not extend it, and does not renumber it: a frame code or a
// capability string that the controller does not know is ignored there, in
// silence, and a device that invents one looks like a device that is broken.
//
// The two rules the contract is built on apply to this package too:
//
//   - Negotiate by capability, never by version. A capability is the device
//     stating what it implements; a version comparison encodes release
//     history into the protocol and misjudges development builds.
//   - Degrade to old behaviour, never to a wrong answer. An absent
//     measurement is not a zero, and an unknown field is not an error.
package protocol

// Capability names a feature a device implements. The controller gates every
// feature on the presence of its string, so a capability announced here must
// mean the device can actually do the thing — announcing one and not
// implementing it produces a device that scores, stays silent, and looks
// broken. That is the failure `oww_shadow` and `oww_trigger` were split to
// avoid.
type Capability string

const (
	// CapMic — the device streams microphone PCM on the data plane.
	CapMic Capability = "mic"
	// CapSpeaker — the device plays speaker PCM from the data plane.
	CapSpeaker Capability = "speaker"
	// CapLEDs — the device accepts `leds` frames.
	CapLEDs Capability = "leds"
	// CapLEDAnim — the device renders an animation locally from one
	// `led_anim` spec per state change.
	CapLEDAnim Capability = "led_anim"
	// CapButtons — the device emits `button` events.
	CapButtons Capability = "buttons"
	// CapOWWShadow — the device can score the wake word locally and report
	// the crossing. Reporting only; it never acts.
	CapOWWShadow Capability = "oww_shadow"
	// CapOWWTrigger — the device can act on its own wake detection. Kept
	// separate from CapOWWShadow because firmware exists that scores and
	// reports but has no code to act: a controller that treated "can score"
	// as "can trigger" would stand down its own detector and wait for a
	// trigger that never comes.
	CapOWWTrigger Capability = "oww_trigger"
	// CapButtonHold — the device emits a long-press duration (`heldMs`).
	CapButtonHold Capability = "button_hold"
	// CapAudioMix — the device holds music on its own frame type and mixes
	// it under voice rather than pausing.
	CapAudioMix Capability = "audio_mix"
	// CapOWWLocalOnly — the device can listen privately: score its own wake
	// word and send nothing until it fires. Whether it IS doing so is
	// reported as `listen_state`, not implied by the capability.
	CapOWWLocalOnly Capability = "oww_local_only"
	// CapAECHWRef — the device can take the acoustic echo cancellation
	// reference from a playback loopback. Whether the board actually has
	// one is a runtime answer reported as `aecRef`.
	CapAECHWRef Capability = "aec_hw_ref"
	// CapOutputChain — the device runs the speaker output chain (EQ, bass
	// guard, limiter) itself at the write, and only when the controller's
	// ack carries `output_chain` too.
	CapOutputChain Capability = "output_chain"
	// CapWakeCue — the device generates its own wake confirmation sound.
	CapWakeCue Capability = "wake_cue"
	// CapVolumeCue — the device previews a volume change as a sound.
	CapVolumeCue Capability = "volume_cue"
	// CapWakeMic — the device can listen on a chosen perimeter microphone.
	CapWakeMic Capability = "wake_mic"
	// CapRemoteVolumeArc — the device can show a level arc for a remote
	// volume change.
	CapRemoteVolumeArc Capability = "remote_volume_arc"
	// CapResponseLevel — the device can apply a relative gain to the voice
	// plane.
	CapResponseLevel Capability = "response_level"
	// CapStockSound — the device voices its own speaker with its own
	// volume law.
	CapStockSound Capability = "stock_sound"
	// CapAmbientLight — the device has a light sensor it can read.
	CapAmbientLight Capability = "ambient_light"
	// CapSendspin — the device can be a Sendspin player for synchronised
	// multi-room audio.
	CapSendspin Capability = "sendspin"
	// CapWakeWordOff — the device honours `wakeWordEnabled: false`.
	CapWakeWordOff Capability = "wake_word_off"
	// CapPairing — the device can ask to pair itself.
	CapPairing Capability = "pairing"
	// CapBLEConnect — the device can hold Bluetooth LE connections for the
	// controller.
	CapBLEConnect Capability = "ble_connect"
)

// Controller feature names are what the controller announces on its `ack`.
// They are the mirror of the capabilities above and are read the same way: a
// feature absent from the ack is one the controller cannot do, and a message
// sent to a side that does not read it vanishes without an error.
const (
	// FeatureBLEAdvertsData — the controller reads BLE advertisement
	// batches off the data plane as 0x06 frames.
	FeatureBLEAdvertsData = "ble_adverts_data"
	// FeatureListenSession — the controller understands private listening:
	// `listen_state`, session-tagged `oww_wake`, 0x07 session audio and the
	// `listen_*` replies.
	FeatureListenSession = "listen_session"
	// FeatureOutputChain — the controller sends unprocessed audio and
	// leaves the output chain to the device.
	FeatureOutputChain = "output_chain"
	// FeatureBLEConnect — the controller drives Bluetooth LE connections
	// through the device as 0x08 frames in both directions.
	FeatureBLEConnect = "ble_connect"
)

// AllCapabilities is every capability string the controller understands. A
// device that sends a string outside this set is silently ignored on that
// feature, so the list is the contract, and a new capability is added here
// when the controller learns it.
var AllCapabilities = []Capability{
	CapMic, CapSpeaker, CapLEDs, CapLEDAnim, CapButtons,
	CapOWWShadow, CapOWWTrigger, CapButtonHold, CapAudioMix,
	CapOWWLocalOnly, CapAECHWRef, CapOutputChain, CapWakeCue,
	CapVolumeCue, CapWakeMic, CapRemoteVolumeArc, CapResponseLevel,
	CapStockSound, CapAmbientLight, CapSendspin, CapWakeWordOff,
	CapPairing, CapBLEConnect,
}

// BaseOS names the userspace a device booted. The controller uses it to gate
// Android-only payloads and to name the model in Home Assistant. It rides
// registration rather than the stats tick, because its consumer runs the
// moment a device connects.
type BaseOS string

const (
	// BaseOSUnknown — the device did not say. The controller treats this
	// as Android for payload gating, but it must not disable a control.
	BaseOSUnknown BaseOS = "unknown"
	// BaseOSEmOS — the device runs EchoMuse's own Linux userspace.
	BaseOSEmOS BaseOS = "emos"
	// BaseOSFireOS — the device runs Amazon's Android with EchoMuse on top.
	BaseOSFireOS BaseOS = "fireos"
)

// Listen states are what a device is doing with its microphone, reported on
// `listen_state`. They are the "is it" that pairs with the `oww_local_only`
// capability's "could it".
const (
	// ListenLocal — the device scores its own wake word and sends nothing
	// until it fires. Audio leaves the device only after the wake word.
	ListenLocal = "local"
	// ListenStream — the device streams continuously to the controller.
	ListenStream = "stream"
	// ListenDegraded — the device cannot run its own wake word. It is
	// button-only and says why.
	ListenDegraded = "degraded"
)

// ButtonClickType is the key code that identifies the action button. The
// controller's button handler recognises this value and no other, so a
// device with a differently-numbered button must still report this one to
// have its presses act.
const ButtonClickType = 138

// Button press states.
const (
	// ButtonDown — the press.
	ButtonDown = true
	// ButtonUp — the release.
	ButtonUp = false
)

// Message types on the control plane. These are the strings the controller
// dispatches on; an unknown type is ignored, which is the forward
// compatibility rule.
const (
	// Device → Controller.
	TypeRegister       = "register"
	TypeIdentify       = "identify"
	TypeButton         = "button"
	TypeMuteState      = "mute_state"
	TypeVolumeState    = "volume_state"
	TypeOWWShadowCross = "oww_shadow_cross"
	TypeOWWWake        = "oww_wake"
	TypeListenState    = "listen_state"
	TypeListenEnd      = "listen_end"
	TypeAmbientLight   = "ambient_light"
	TypeStats          = "stats"
	TypeBLEAdverts     = "ble_adverts"
	TypeWifiScanResult = "wifi_scan_result"
	TypeWifiResult     = "wifi_result"
	TypePairRequest    = "pair_request"
	TypeSendspinStatus = "sendspin_status"
	TypeSendspinToken  = "sendspin_token"
	TypePlaybackStats  = "playback_stats"
	TypeLog            = "log"
	TypePong           = "pong"

	// Controller → Device.
	TypeAck                  = "ack"
	TypeRefused              = "refused"
	TypePending              = "pending"
	TypeLEDs                 = "leds"
	TypeLEDAnim              = "led_anim"
	TypeMicStart             = "mic_start"
	TypeMicStop              = "mic_stop"
	TypeListenAck            = "listen_ack"
	TypeListenClose          = "listen_close"
	TypeBeamLock             = "beam_lock"
	TypeBeamUnlock           = "beam_unlock"
	TypeVolumeSet            = "volume_set"
	TypeDuck                 = "duck"
	TypeConfig               = "config"
	TypeWifiScan             = "wifi_scan"
	TypeWifiChange           = "wifi_change"
	TypeWifiCommit           = "wifi_commit"
	TypeShellOpen            = "shell_open"
	TypeShellClose           = "shell_close"
	TypePlayCue              = "play_cue"
	TypeMusicFlush           = "music_flush"
	TypeSpeakerFlush         = "speaker_flush"
	TypeSendspinTokenRequest = "sendspin_token_request"
	TypePing                 = "ping"
)
