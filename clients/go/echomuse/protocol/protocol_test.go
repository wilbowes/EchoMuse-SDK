package protocol_test

import (
	"encoding/json"
	"testing"

	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/protocol"
)

// TestRegisterCarriesTheNegotiation checks that a registration carries
// what the controller reads from it.
func TestRegisterCarriesTheNegotiation(t *testing.T) {
	reg := protocol.Register{
		Type:         protocol.TypeRegister,
		DeviceID:     "device-1",
		Version:      "1.0.0",
		Capabilities: []protocol.Capability{protocol.CapMic, protocol.CapSpeaker},
		Board:        "pi-touch",
	}

	raw, err := json.Marshal(reg)
	if err != nil {
		t.Fatalf("encoding the registration: %v", err)
	}

	var back map[string]any
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("decoding the registration: %v", err)
	}

	if back["type"] != protocol.TypeRegister {
		t.Errorf("the type was %v, want %q", back["type"], protocol.TypeRegister)
	}
	if back["device_id"] != "device-1" {
		t.Errorf("the device id was %v", back["device_id"])
	}

	caps, ok := back["capabilities"].([]any)
	if !ok {
		t.Fatalf("the capabilities were %T, want a list", back["capabilities"])
	}
	if len(caps) != 2 {
		t.Errorf("the registration carried %d capabilities, want 2", len(caps))
	}
}

// TestRegisterOmitsWhatItCannotMeasure checks that an unset optional field
// is absent rather than zero.
//
// A field the device cannot read is omitted. A zero would say "measured as
// zero", which is a different and wrong answer.
func TestRegisterOmitsWhatItCannotMeasure(t *testing.T) {
	reg := protocol.Register{
		Type:     protocol.TypeRegister,
		DeviceID: "device-1",
	}

	raw, err := json.Marshal(reg)
	if err != nil {
		t.Fatalf("encoding the registration: %v", err)
	}

	var back map[string]any
	json.Unmarshal(raw, &back)

	for _, key := range []string{"board", "ip", "boot_id", "kernel_arch"} {
		if _, present := back[key]; present {
			t.Errorf("the registration carried %q, which it cannot measure", key)
		}
	}
}

// TestAckFeatures checks that the controller's feature list is read the
// same way as the device's capabilities.
func TestAckFeatures(t *testing.T) {
	ack := protocol.Ack{
		Type:     protocol.TypeAck,
		Features: []string{protocol.FeatureListenSession, protocol.FeatureOutputChain},
	}

	if !ack.HasFeature(protocol.FeatureListenSession) {
		t.Error("the ack did not report the listen session feature")
	}
	if ack.HasFeature(protocol.FeatureBLEConnect) {
		t.Error("the ack reported a feature it was not given")
	}
}

// TestConfigKeepsFalseAndZero checks the pointer fields.
//
// For these keys the difference between "off" and "not sent" is the
// difference between a setting the user chose and a setting nobody
// touched, so false and zero have to survive the round trip.
func TestConfigKeepsFalseAndZero(t *testing.T) {
	wakeWord := false
	timeout := 0
	duck := 0.0

	cfg := protocol.Config{
		WakeWordEnabled:   &wakeWord,
		ConsoleTimeoutMin: &timeout,
		DuckDb:            &duck,
	}

	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("encoding the config: %v", err)
	}

	back, err := protocol.DecodeConfig(raw)
	if err != nil {
		t.Fatalf("decoding the config: %v", err)
	}

	if back.WakeWordEnabled == nil || *back.WakeWordEnabled != false {
		t.Error("the wake word setting did not survive as false")
	}
	if back.ConsoleTimeoutMin == nil || *back.ConsoleTimeoutMin != 0 {
		t.Error("the console timeout did not survive as zero")
	}
	if back.DuckDb == nil || *back.DuckDb != 0.0 {
		t.Error("the duck level did not survive as zero")
	}
}

// TestConfigOmitsWhatWasNotSent checks that an absent key stays absent.
func TestConfigOmitsWhatWasNotSent(t *testing.T) {
	raw := []byte(`{"type":"config","owwThreshold":0.5}`)

	cfg, err := protocol.DecodeConfig(raw)
	if err != nil {
		t.Fatalf("decoding the config: %v", err)
	}

	if cfg.OwwThreshold != 0.5 {
		t.Errorf("the threshold was %v, want 0.5", cfg.OwwThreshold)
	}
	if cfg.OwwModel != "" {
		t.Errorf("the model was %q, want it absent", cfg.OwwModel)
	}
	if cfg.WakeWordEnabled != nil {
		t.Error("the wake word setting appeared when it was not sent")
	}
}

// TestFrameSentinelsAreDirectional checks the capture and playback frames.
//
// The codes are namespaced by direction, so 0x04 and 0x05 are reused across
// the two directions on purpose — a capture frame and a playback frame never
// meet on the same wire. What must not collide is two frames in the SAME
// direction.
func TestFrameSentinelsAreDirectional(t *testing.T) {
	capture := map[byte]string{
		protocol.FrameVADEnd:          "the VAD-end sentinel",
		protocol.FrameNoSpeechTimeout: "the no-speech sentinel",
		protocol.FrameMic:             "the microphone frame",
		protocol.FrameListen:          "the session-audio frame",
		protocol.FrameBLEAdverts:      "the BLE advertisement frame",
		protocol.FrameBLEGATT:         "the BLE GATT frame",
	}
	playback := map[byte]string{
		protocol.FrameSpeaker:    "the speaker frame",
		protocol.FrameSpeakerEOS: "the speaker end frame",
		protocol.FrameMusic:      "the music frame",
		protocol.FrameMusicEOS:   "the music end frame",
	}

	// The two directions are separate tables, so a shared code between
	// them is the design rather than a collision.
	if protocol.FrameVADEnd != protocol.FrameMusic {
		t.Error("the VAD-end sentinel and the music frame do not share a code")
	}
	if protocol.FrameNoSpeechTimeout != protocol.FrameMusicEOS {
		t.Error("the no-speech sentinel and the music end frame do not share a code")
	}
	if protocol.FrameSpeaker == protocol.FrameSpeakerEOS {
		t.Error("the speaker and speaker-end frames share a code")
	}

	// Within one direction, no two frames may share a code.
	for code, name := range capture {
		for other, otherName := range capture {
			if code == other && name != otherName {
				t.Errorf("two capture frames share the code 0x%02x: %s and %s", code, name, otherName)
			}
		}
	}
	for code, name := range playback {
		for other, otherName := range playback {
			if code == other && name != otherName {
				t.Errorf("two playback frames share the code 0x%02x: %s and %s", code, name, otherName)
			}
		}
	}
}

// TestSpeakerFrameLayout checks that a playback frame is the type byte
// followed by the samples.
func TestSpeakerFrameLayout(t *testing.T) {
	pcm := []byte{0x01, 0x02, 0x03}
	frame := protocol.EncodeSpeaker(pcm)

	if frame[0] != protocol.FrameSpeaker {
		t.Errorf("the type byte was 0x%02x, want 0x%02x", frame[0], protocol.FrameSpeaker)
	}
	if len(frame) != 1+len(pcm) {
		t.Errorf("the frame was %d bytes, want %d", len(frame), 1+len(pcm))
	}
}

// TestUnknownFrameIsReported checks that an unknown frame type is
// reported rather than guessed at.
func TestUnknownFrameIsReported(t *testing.T) {
	_, _, err := protocol.ParseFrame([]byte{0x7f, 0x00})
	if err == nil {
		t.Error("an unknown frame type was accepted")
	}
}

// TestAudioRatesAreAsymmetric pins the two rates.
//
// Capture is 16 kHz and playback is 48 kHz, and a device that gets this
// wrong plays audio at the wrong speed.
func TestAudioRatesAreAsymmetric(t *testing.T) {
	if protocol.MicSampleRate != 16000 {
		t.Errorf("the capture rate was %d, want 16000", protocol.MicSampleRate)
	}
	if protocol.SpeakerSampleRate != 48000 {
		t.Errorf("the playback rate was %d, want 48000", protocol.SpeakerSampleRate)
	}
}

// TestButtonClickTypeIsTheActionButton pins the one code the controller
// recognises.
//
// A device with a differently-numbered button must still report this one,
// or its presses are ignored.
func TestButtonClickTypeIsTheActionButton(t *testing.T) {
	if protocol.ButtonClickType != 138 {
		t.Errorf("the action button code was %d, want 138", protocol.ButtonClickType)
	}
}

// TestIncomingDecodes checks the escape hatch for a message the SDK does
// not model.
func TestIncomingDecodes(t *testing.T) {
	raw := json.RawMessage(`{"type":"something_new","value":42}`)

	msg := protocol.Incoming{Type: "something_new", Raw: raw}
	var out struct {
		Value int `json:"value"`
	}
	if err := msg.Decode(&out); err != nil {
		t.Fatalf("decoding the message: %v", err)
	}
	if out.Value != 42 {
		t.Errorf("the value was %d, want 42", out.Value)
	}
}

// TestIncomingWithoutABodyFails checks that a message with no body is an
// error rather than a silent zero.
func TestIncomingWithoutABodyFails(t *testing.T) {
	msg := protocol.Incoming{Type: "something_new"}
	var out struct{}
	if err := msg.Decode(&out); err == nil {
		t.Error("a message with no body decoded without error")
	}
}
