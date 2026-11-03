package protocol

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Frame codes on the data plane.
//
// The codes are namespaced by direction and that is deliberate: 0x04 and
// 0x05 mean different things depending on who sent them. A device must not
// read them from one global table.
const (
	// Device → Controller.
	FrameMic             byte = 0x01
	FrameVADEnd          byte = 0x04
	FrameNoSpeechTimeout byte = 0x05
	FrameBLEAdverts      byte = 0x06
	FrameListen          byte = 0x07
	FrameBLEGATT         byte = 0x08

	// Controller → Device.
	FrameSpeaker    byte = 0x02
	FrameSpeakerEOS byte = 0x03
	FrameMusic      byte = 0x04
	FrameMusicEOS   byte = 0x05
)

// Audio formats. The two rates differ, and the difference matters: a device
// that captures at one rate and is handed the other produces audio that
// plays at the wrong speed.
const (
	// MicSampleRate is the rate the controller expects captured audio at.
	MicSampleRate = 16000
	// SpeakerSampleRate is the rate the controller sends playback at.
	SpeakerSampleRate = 48000
	// MicChannels — the capture plane is mono.
	MicChannels = 1
	// SpeakerChannels — the playback plane is mono.
	SpeakerChannels = 1
	// MicBytesPerSample — signed 16-bit little-endian.
	MicBytesPerSample = 2
	// SpeakerBytesPerSample — signed 16-bit little-endian.
	SpeakerBytesPerSample = 2
)

// MicHeaderLen is the length of the header on a captured-audio frame:
// one type byte and a 16-bit sequence number.
const MicHeaderLen = 3

// ListenHeaderLen is the length of the header on a private-listening audio
// frame: one type byte, a 32-bit session number and a 16-bit sequence
// number.
const ListenHeaderLen = 7

// ErrShortFrame is returned when a frame is shorter than its header allows.
var ErrShortFrame = errors.New("echomuse: frame shorter than its header")

// MicFrame is a captured-audio frame on its way to the controller.
//
// The sequence number is per connection and increments on every frame, so a
// gap is visible to the controller as a gap rather than as silence.
type MicFrame struct {
	Seq uint16
	PCM []byte
}

// Encode builds the wire form: the type byte, the sequence number in big
// endian, then the samples.
func (f MicFrame) Encode() []byte {
	out := make([]byte, MicHeaderLen+len(f.PCM))
	out[0] = FrameMic
	binary.BigEndian.PutUint16(out[1:3], f.Seq)
	copy(out[MicHeaderLen:], f.PCM)
	return out
}

// ParseMicFrame reads a captured-audio frame.
func ParseMicFrame(data []byte) (MicFrame, error) {
	if len(data) < MicHeaderLen {
		return MicFrame{}, fmt.Errorf("%w: got %d bytes, want at least %d",
			ErrShortFrame, len(data), MicHeaderLen)
	}
	return MicFrame{
		Seq: binary.BigEndian.Uint16(data[1:3]),
		PCM: data[MicHeaderLen:],
	}, nil
}

// ListenFrame is private-listening audio on its way to the controller.
//
// The frames of one session are numbered from zero, so a gap inside a
// session is visible as a gap.
type ListenFrame struct {
	Session uint32
	Seq     uint16
	PCM     []byte
}

// Encode builds the wire form: the type byte, the session number and the
// sequence number in big endian, then the samples.
func (f ListenFrame) Encode() []byte {
	out := make([]byte, ListenHeaderLen+len(f.PCM))
	out[0] = FrameListen
	binary.BigEndian.PutUint32(out[1:5], f.Session)
	binary.BigEndian.PutUint16(out[5:7], f.Seq)
	copy(out[ListenHeaderLen:], f.PCM)
	return out
}

// ParseListenFrame reads a private-listening audio frame.
func ParseListenFrame(data []byte) (ListenFrame, error) {
	if len(data) < ListenHeaderLen {
		return ListenFrame{}, fmt.Errorf("%w: got %d bytes, want at least %d",
			ErrShortFrame, len(data), ListenHeaderLen)
	}
	return ListenFrame{
		Session: binary.BigEndian.Uint32(data[1:5]),
		Seq:     binary.BigEndian.Uint16(data[5:7]),
		PCM:     data[ListenHeaderLen:],
	}, nil
}

// ParseFrame identifies a frame the controller sent.
//
// The return value is the frame type and its payload with the type byte
// removed. An unknown type is returned as-is with ErrUnknownFrame, because
// the contract says unknown frame types are ignored in both directions — the
// caller decides whether that is worth reporting.
func ParseFrame(data []byte) (byte, []byte, error) {
	if len(data) == 0 {
		return 0, nil, fmt.Errorf("%w: empty frame", ErrShortFrame)
	}
	switch data[0] {
	case FrameSpeaker, FrameSpeakerEOS, FrameMusic, FrameMusicEOS, FrameBLEGATT:
		return data[0], data[1:], nil
	}
	return data[0], data[1:], fmt.Errorf("%w: 0x%02x", ErrUnknownFrame, data[0])
}

// ErrUnknownFrame is returned for a frame type the SDK does not know.
var ErrUnknownFrame = errors.New("echomuse: unknown frame type")

// EncodeSpeaker builds a playback frame.
func EncodeSpeaker(pcm []byte) []byte {
	out := make([]byte, 1+len(pcm))
	out[0] = FrameSpeaker
	copy(out[1:], pcm)
	return out
}

// EncodeSpeakerEOS builds the end-of-playback frame.
func EncodeSpeakerEOS() []byte { return []byte{FrameSpeakerEOS} }

// EncodeMusic builds a music frame.
func EncodeMusic(pcm []byte) []byte {
	out := make([]byte, 1+len(pcm))
	out[0] = FrameMusic
	copy(out[1:], pcm)
	return out
}

// EncodeMusicEOS builds the end-of-music frame.
func EncodeMusicEOS() []byte { return []byte{FrameMusicEOS} }

// EncodeVADEnd builds the sentinel that ends a bounded turn: speech was
// detected, and then ended.
//
// It is distinct from the no-speech sentinel on purpose. "The wake word then
// silence" — quietly give up — has to be tellable from "spoke, and the
// backend had nothing to say".
func EncodeVADEnd() []byte { return []byte{FrameVADEnd} }

// EncodeNoSpeechTimeout builds the sentinel that ends a bounded turn in
// which no speech was ever detected.
func EncodeNoSpeechTimeout() []byte { return []byte{FrameNoSpeechTimeout} }
