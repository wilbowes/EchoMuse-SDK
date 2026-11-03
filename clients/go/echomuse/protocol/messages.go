package protocol

import (
	"encoding/json"
	"fmt"
)

// Identify is the first message a device sends on the data plane.
//
// The data plane carries audio, which is not self-describing: a frame of
// PCM belongs to a device, and the controller learns which one from this
// message. It reads the first frame of the connection to do that, and
// closes a data plane whose first frame is anything else — including a mic
// frame, which it has no device to route.
type Identify struct {
	Type     string `json:"type"`
	DeviceID string `json:"device_id"`
}

// Register is the first message a device sends, once per control-plane
// connection. It names the device, says what it can do, and describes the
// boot.
//
// The controller reads the capabilities to gate every feature, so this
// message is the device's whole negotiation with it. Nothing here is
// required except the identity and the capabilities: a field the device
// cannot read is omitted rather than sent as a zero, because absence means
// "not measured" and a zero would mean "measured as zero".
type Register struct {
	Type         string       `json:"type"`
	DeviceID     string       `json:"device_id"`
	Version      string       `json:"version"`
	Capabilities []Capability `json:"capabilities"`

	// IP is the device's local address, omitted when it is loopback or
	// unresolved.
	IP string `json:"ip,omitempty"`

	// AmbientLightStatus explains why the ambient light sensor is or is
	// not available. A capability list says WHAT a device has; when the
	// answer is "nothing", nobody can tell an absent chip from an unbound
	// driver without a shell session on the hardware.
	AmbientLightStatus map[string]any `json:"ambient_light_status,omitempty"`

	// BaseOS names the userspace the device booted. It rides registration
	// rather than the stats tick because its consumer — the payload
	// reconcile — runs the moment a device connects, long before the first
	// stats report.
	BaseOS BaseOS `json:"base_os,omitempty"`

	// Pairing asks to pair. It is set only during a pairing window.
	Pairing bool `json:"pairing,omitempty"`

	// Board identifies the hardware. The controller names the model in
	// Home Assistant from it, and an unknown id falls back to the Echo Dot's
	// model.
	Board string `json:"board,omitempty"`

	// KernelArch and KernelRelease are the running kernel's `uname -m` and
	// `uname -r`, omitted if `uname` fails.
	KernelArch    string `json:"kernel_arch,omitempty"`
	KernelRelease string `json:"kernel_release,omitempty"`

	// BootID is the kernel's /proc/sys/kernel/random/boot_id. The
	// controller keeps one row per boot rather than per redial.
	BootID string `json:"boot_id,omitempty"`

	// BootReason is why the device started.
	BootReason string `json:"boot_reason,omitempty"`

	// EMMC is the eMMC's wear report.
	EMMC map[string]any `json:"emmc,omitempty"`
}

// Ack is the controller's answer to a registration. It carries the
// controller's own capability list, read the same way as the device's: a
// feature absent from the list is one the controller cannot do, and a
// message sent to a side that does not read it vanishes.
//
// The list is absent entirely on controllers older than 2.23.0, which is
// why every feature check must treat an empty list as "nothing negotiated"
// rather than "nothing supported".
type Ack struct {
	Type     string   `json:"type"`
	DeviceID string   `json:"device_id"`
	Features []string `json:"features"`
	TimeMs   int64    `json:"time_ms,omitempty"`
	Pairing  bool     `json:"pairing,omitempty"`
}

// HasFeature reports whether the controller announced a feature.
func (a Ack) HasFeature(name string) bool {
	for _, f := range a.Features {
		if f == name {
			return true
		}
	}
	return false
}

// ButtonEvent is a press or release of the action button.
//
// The release carries the duration, measured on the device. Timing it on the
// controller instead would put the gesture at the mercy of round-trip time
// excursions, and a hold read as a tap — or the reverse — is a gesture the
// user did not make. An absent HeldMs reads as a tap, which is the safe
// direction: an unknown hold time is never promoted to a gesture.
type ButtonEvent struct {
	Type      string `json:"type"`
	ClickType int    `json:"clickType"`
	Down      bool   `json:"down"`
	HeldMs    int64  `json:"heldMs,omitempty"`
	Muted     bool   `json:"muted"`
}

// MuteState reports the device's mute switch. Mute is device-sovereign: the
// device holds the authority and the controller is told, never the reverse.
type MuteState struct {
	Type  string `json:"type"`
	Muted bool   `json:"muted"`
}

// VolumeState reports a volume change, which the controller persists as the
// device's startup volume.
type VolumeState struct {
	Type  string `json:"type"`
	Level int    `json:"level"`
}

// OWWWake says the device's own wake word detector fired.
//
// Under private listening this opens a session and the audio follows as
// 0x07 frames; without a session it lands as a pending wake and the
// continuous stream carries the audio.
type OWWWake struct {
	Type         string  `json:"type"`
	Score        float32 `json:"score"`
	Threshold    float32 `json:"threshold"`
	AgeMs        int64   `json:"ageMs,omitempty"`
	CapturedMono int64   `json:"capturedMono,omitempty"`

	// Session is non-zero when this wake opened a private-listening
	// session.
	Session uint32 `json:"session,omitempty"`
	// Floor is the score below which the wake would have been rejected.
	Floor float64 `json:"floor,omitempty"`
	// Barge marks a wake heard over the device's own playback.
	Barge bool `json:"barge,omitempty"`

	// Level and Peak are the wake word's loudness in dBFS with the
	// microphone gain removed, over the two seconds ending at the crossing
	// frame. Reported, not acted on.
	Level *float32 `json:"level,omitempty"`
	Peak  *float32 `json:"peak,omitempty"`
}

// OWWShadowCross reports a crossing in shadow mode: the device scored, and
// deliberately did not act on it.
type OWWShadowCross struct {
	Type  string  `json:"type"`
	Score float32 `json:"score"`
	AgeMs int64   `json:"ageMs,omitempty"`
}

// ListenState reports what the device is actually doing with its microphone.
// Sent on every change and after every ack, because the controller keeps no
// memory of it across a reconnect.
type ListenState struct {
	Type   string `json:"type"`
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}

// ListenEnd reports a session the device closed itself, with the reason.
type ListenEnd struct {
	Type    string `json:"type"`
	Session uint32 `json:"session"`
	Reason  string `json:"reason"`
}

// ListenAck acknowledges a private-listening wake, ending the device's
// acknowledgement clock.
type ListenAck struct {
	Type    string `json:"type"`
	Session uint32 `json:"session"`
}

// ListenClose ends a private-listening session.
type ListenClose struct {
	Type    string `json:"type"`
	Session uint32 `json:"session"`
	Reason  string `json:"reason,omitempty"`
}

// SendspinStatus reports the player's current state. Status is public player state; credentials belong in SendspinToken only.
type SendspinStatus struct {
	Type   string `json:"type"`
	Status any    `json:"status"`
}

// SendspinToken carries the short-lived player token requested by the controller. Never log this message.
type SendspinToken struct {
	Type     string `json:"type"`
	Token    string `json:"token,omitempty"`
	ClientID string `json:"clientId,omitempty"`
	Error    string `json:"error,omitempty"`
}

// SendspinTokenRequest asks the player integration for its current token.
type SendspinTokenRequest struct {
	Type string `json:"type"`
}

// PlaybackStats summarizes one drained speaker stream.
type PlaybackStats struct {
	Type      string                 `json:"type"`
	Periods   *int                   `json:"periods,omitempty"`
	Underruns *int                   `json:"underruns,omitempty"`
	Stats     *PlaybackDeliveryStats `json:"stats,omitempty"`
	Barge     map[string]any         `json:"barge,omitempty"`
}

// PlaybackDeliveryStats reports buffering and delivery margin from the audio backend.
type PlaybackDeliveryStats struct {
	MinDepth    *int   `json:"minDepth,omitempty"`
	PrimeWaitMs *int   `json:"primeWaitMs,omitempty"`
	RecvSpanMs  *int   `json:"recvSpanMs,omitempty"`
	MaxGapMs    *int   `json:"maxGapMs,omitempty"`
	BytesRecv   *int64 `json:"bytesRecv,omitempty"`
}

// OWWShadowStats is the rolling diagnostic window for local wake inference.
type OWWShadowStats struct {
	Frames     *uint64  `json:"frames,omitempty"`
	Drops      *uint64  `json:"drops,omitempty"`
	Errors     *uint64  `json:"errors,omitempty"`
	Crossings  *uint64  `json:"crossings,omitempty"`
	MaxScore   *float64 `json:"maxScore,omitempty"`
	Threshold  *float64 `json:"threshold,omitempty"`
	MaxInferMs *int     `json:"maxInferMs,omitempty"`
	MaxGapMs   *int     `json:"maxGapMs,omitempty"`
	LastErr    string   `json:"lastErr,omitempty"`
}

// LEDs is one LED frame. The controller renders every animation frame and
// the device just paints it, so the frame is a complete picture of the ring
// at that instant.
type LEDs struct {
	Type string `json:"type"`
	// LEDs is one entry per indicator: an id and a colour.
	LEDs []LED `json:"leds"`
	// Listening marks the listening ring, which the direction overlay
	// keys off.
	Listening *bool `json:"listening,omitempty"`
}

// LED is one indicator's colour.
type LED struct {
	ID int `json:"id"`
	R  int `json:"r"`
	G  int `json:"g"`
	B  int `json:"b"`
}

// LEDAnim is an animation spec the device renders locally, on its own
// ticker, until a newer spec replaces it or its dead-man TTL expires.
//
// A device that renders locally keeps a smooth ring when the controller's
// event loop stalls or the WiFi jitters, and a controller that dies
// mid-animation cannot leave the ring lit.
type LEDAnim struct {
	Type string   `json:"type"`
	Anim AnimSpec `json:"anim"`
}

// AnimSpec describes a device-rendered animation.
//
// The patterns are the same set the Echo's firmware renders, so a device
// that wants the same look implements the same six. A pattern outside the
// set is ignored, and the ring is cleared.
type AnimSpec struct {
	// Pattern is one of "off", "solid", "spin", "rotate", "pulse" or
	// "meter".
	Pattern string `json:"pattern"`

	// Colors carries the palette. Its meaning depends on the pattern:
	//   solid       — the palette painted one-to-one, so a single colour
	//                 is the whole ring and several are per-indicator
	//   spin        — [head, trail]
	//   rotate      — a palette rotated one indicator per frame
	//   pulse, meter — a palette whose brightness is modulated
	Colors [][3]uint8 `json:"colors"`

	// PeriodMs is the frame interval for spin and rotate, and the full
	// throb cycle for pulse. Zero means the default.
	PeriodMs int `json:"periodMs,omitempty"`

	// Listening marks a solid frame as the listening ring.
	Listening bool `json:"listening,omitempty"`

	// TTLSec clears the ring when no newer spec arrives. It is the
	// dead-man switch against a controller that died mid-turn.
	TTLSec int `json:"ttlSec,omitempty"`

	// The meter curve. Pointer-typed so zero is expressible and absent is
	// distinguishable from zero.
	Attack *float64 `json:"attack,omitempty"`
	Decay  *float64 `json:"decay,omitempty"`
	Floor  *float64 `json:"floor,omitempty"`
	Gamma  *float64 `json:"gamma,omitempty"`
	Ref    *float64 `json:"ref,omitempty"`
	Curve  *float64 `json:"curve,omitempty"`
}

// MicStart asks the device to stream microphone audio.
//
// Without LockMic the stream is the always-on wake stream: ungated and
// AGC-free, so the wake scorer sees an uninterrupted stream. With LockMic it
// is a bounded turn, gated on speech and ended by the device with a
// sentinel.
type MicStart struct {
	Type    string `json:"type"`
	LockMic bool   `json:"lock_mic,omitempty"`
}

// MicStop stops the microphone stream.
//
// Under private listening it ends a bounded turn but not a session — only
// `listen_close` ends a session, and not local listening, which is what
// hears a barge-in.
type MicStop struct {
	Type string `json:"type"`
}

// VolumeSet sets an absolute volume level.
type VolumeSet struct {
	Type  string `json:"type"`
	Level int    `json:"level"`
}

// Duck asks the device to duck music under a voice turn.
type Duck struct {
	Type string `json:"type"`
	On   bool   `json:"on"`
}

// PlayCue asks the device to play a sound it generates itself.
type PlayCue struct {
	Type string `json:"type"`
	Cue  string `json:"cue"`
}

// ShellOpen asks the device to dial the shell plane.
type ShellOpen struct {
	Type string `json:"type"`
	Pty  bool   `json:"pty,omitempty"`
}

// ShellClose asks the device to close its shell session.
type ShellClose struct {
	Type string `json:"type"`
}

// Ping is the controller's keepalive.
type Ping struct {
	Type string `json:"type"`
	// ID is the controller's sequence number for this ping.
	//
	// It is a number on the wire, and it is carried as raw JSON rather than
	// a typed field on purpose: the answer has to echo the controller's own
	// value back byte for byte, and a field typed as a string or an int
	// cannot hold an id whose type it does not know. A ping that arrives
	// without an id is answered with a pong that has none, which is what
	// the controller expects from a keepalive.
	ID json.RawMessage `json:"id,omitempty"`
}

// Pong answers a ping. Mono is the device's monotonic clock in
// milliseconds, any fixed origin — the controller maps it onto its own to
// date the captured audio.
type Pong struct {
	Type string `json:"type"`
	// ID echoes the controller's ping id. It is raw JSON for the same
	// reason the ping's is: the answer has to match the question, and a
	// typed field would change the id's type on the way back.
	ID   json.RawMessage `json:"id,omitempty"`
	Mono int64           `json:"mono,omitempty"`
}

// Log is a device log line. The controller persists it and pushes it to the
// dashboard.
type Log struct {
	Type    string `json:"type"`
	Level   string `json:"level"`
	Message string `json:"message"`
}

// AmbientLight reports a light reading.
type AmbientLight struct {
	Type string `json:"type"`
	Lux  int    `json:"lux"`
}

// Stats is the periodic hardware and link telemetry, sent about every thirty
// seconds and once on connect.
//
// Every field is optional. Absence means "not measured", never zero — a
// device that cannot report a thing must not read as having reported zero.
type Stats struct {
	Type string `json:"type"`

	CPUPct         float64 `json:"cpuPct,omitempty"`
	MemUsedMb      int     `json:"memUsedMb,omitempty"`
	MemTotalMb     int     `json:"memTotalMb,omitempty"`
	StorageUsedMb  int     `json:"storageUsedMb,omitempty"`
	StorageTotalMb int     `json:"storageTotalMb,omitempty"`

	WifiRssi      *int   `json:"wifiRssi,omitempty"`
	WifiSsid      string `json:"wifiSsid,omitempty"`
	LinkSpeedMbps *int   `json:"linkSpeedMbps,omitempty"`
	WifiFreqMhz   *int   `json:"wifiFreqMhz,omitempty"`
	WifiBssid     string `json:"wifiBssid,omitempty"`

	TxBytes      uint64         `json:"txBytes,omitempty"`
	RxBytes      uint64         `json:"rxBytes,omitempty"`
	TxErrors     uint64         `json:"txErrors,omitempty"`
	TxDropped    uint64         `json:"txDropped,omitempty"`
	RxCrcErrors  uint64         `json:"rxCrcErrors,omitempty"`
	TCPUpRetrans *uint64        `json:"tcpUpRetrans,omitempty"`
	TCPUpSegs    *uint64        `json:"tcpUpSegs,omitempty"`
	BLE          map[string]any `json:"ble,omitempty"`

	// AecRef is which echo-cancellation reference is in force: "hw", "sw"
	// or "off". Never omitted when the device can report it, because
	// "off" collapsing into absent would tell the dashboard a disarmed
	// reference is an unknown one.
	AecRef string `json:"aecRef,omitempty"`

	AmbientLux       *int            `json:"ambientLux,omitempty"`
	CPUTempC         *float64        `json:"cpuTempC,omitempty"`
	MaxTempC         *float64        `json:"maxTempC,omitempty"`
	CoresOnline      *int            `json:"coresOnline,omitempty"`
	CoresTotal       *int            `json:"coresTotal,omitempty"`
	ThermalCoreLimit *int            `json:"thermalCoreLimit,omitempty"`
	OWWShadow        *OWWShadowStats `json:"owwShadow,omitempty"`
	Sendspin         any             `json:"sendspin,omitempty"`
}

// Refused says the device was not admitted: the link credentials were
// refused. A device that can pair shows this as a prompt to pair.
type Refused struct {
	Type string `json:"type"`
}

// Pending says the device is not admitted yet: it is unapproved, or its
// pairing request is waiting for an administrator.
type Pending struct {
	Type    string `json:"type"`
	Pairing bool   `json:"pairing,omitempty"`
}

// Incoming is any control-plane message from the controller. The SDK hands
// these to the application as they arrive, so an application can react to a
// message the SDK does not itself model — a new message type is added by
// the controller over time, and ignoring it silently is the correct
// degrade, but seeing it is better.
type Incoming struct {
	Type string          `json:"type"`
	Raw  json.RawMessage `json:"-"`
}

// Decode unmarshals the raw message into a typed value.
func (m Incoming) Decode(v any) error {
	if len(m.Raw) == 0 {
		return fmt.Errorf("echomuse: message %q carried no body", m.Type)
	}
	return json.Unmarshal(m.Raw, v)
}
