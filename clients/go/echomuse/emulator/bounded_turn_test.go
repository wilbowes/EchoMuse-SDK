package emulator_test

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/emulator"
	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/mockcontroller"
	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/vad"
)

// speakingAudio is a microphone that speaks and then falls silent.
//
// It is the shape of a real turn: somebody answers the question, and then
// stops talking. The silence afterwards is what the device has to notice.
type speakingAudio struct {
	mu sync.Mutex
	// speechFrames is how many frames are speech before the room falls
	// quiet. Zero means the room is quiet throughout.
	speechFrames int
	// frames is how many frames have been produced.
	frames int
	// open is how many captures are open.
	open int
	// done is closed when the capture is closed.
	done chan struct{}
}

// Capture returns a reader that speaks and then falls quiet.
func (a *speakingAudio) Capture(ctx context.Context, rate int) (io.ReadCloser, error) {
	a.mu.Lock()
	a.frames = 0
	a.open++
	a.done = make(chan struct{})
	done := a.done
	a.mu.Unlock()

	return &speakingReader{audio: a, done: done}, nil
}

// Playback returns a writer that discards.
func (a *speakingAudio) Playback(context.Context, int) (io.WriteCloser, error) {
	return nopWriteCloser{}, nil
}

// Describe says what the backend is.
func (a *speakingAudio) Describe() string { return "speaking" }

// openCaptures returns how many captures are still open.
func (a *speakingAudio) openCaptures() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.open
}

// speakingReader produces speech for a while and then silence.
type speakingReader struct {
	audio *speakingAudio
	done  chan struct{}
	once  sync.Once
}

// Read produces a frame of speech or of silence.
//
// The speech lasts long enough for the detector to be sure there is a
// sentence, and then the room falls quiet — which is the part the device
// has to notice and act on.
func (r *speakingReader) Read(p []byte) (int, error) {
	select {
	case <-r.done:
		return 0, io.EOF
	default:
	}

	r.audio.mu.Lock()
	r.audio.frames++
	frames := r.audio.frames
	speechFrames := r.audio.speechFrames
	r.audio.mu.Unlock()

	// The first frames are speech, and the rest is silence.
	loud := frames <= speechFrames
	amplitude := int16(0)
	if loud {
		amplitude = 3000
	}
	for i := 0; i+1 < len(p); i += 2 {
		v := amplitude
		if (i/2)%20 >= 10 {
			v = -v
		}
		p[i] = byte(v)
		p[i+1] = byte(uint16(v) >> 8)
	}

	// The frames arrive at the rate they would from a real capture: a
	// frame is 100ms of audio, and it takes 100ms to arrive.
	time.Sleep(100 * time.Millisecond)
	return len(p), nil
}

// Close ends the reader.
func (r *speakingReader) Close() error {
	r.once.Do(func() {
		r.audio.mu.Lock()
		r.audio.open--
		r.audio.mu.Unlock()
		close(r.done)
	})
	return nil
}

// TestABoundedTurnEndsWithoutMicStop checks that the device ends a
// bounded turn by itself.
//
// The controller asked for the microphone with `lock_mic:true` and then
// said nothing. A device that waits for `mic_stop` keeps the room
// streaming — the controller has moved on and nothing ends the turn.
//
// The device's own limits end it: the speech stops, five seconds of
// silence follows, and the turn is over.
func TestABoundedTurnEndsWithoutMicStop(t *testing.T) {
	mock := mockcontroller.New(mockcontroller.Options{Mode: mockcontroller.ModeApprove})
	defer mock.Close()

	audio := &speakingAudio{speechFrames: 10}

	emu, err := emulator.New(emulator.Options{
		DeviceID:       "bounded-turn",
		ControllerURL:  mock.URL(),
		CredentialsDir: t.TempDir(),
		Audio:          audio,
	})
	if err != nil {
		t.Fatalf("building the emulator: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go emu.Run(ctx)

	waitFor(t, "the link to come up", func() bool {
		return emu.Device().State().Snapshot().LinkUp
	})

	// The controller asks for the microphone and then says nothing more.
	// It never sends `mic_stop`.
	if err := mock.RequestMic(true); err != nil {
		t.Fatalf("asking for the microphone: %v", err)
	}

	waitFor(t, "the capture to open", func() bool {
		return audio.openCaptures() > 0
	})

	// The turn ends by itself, and the microphone is given back.
	waitFor(t, "the bounded turn to end", func() bool {
		return audio.openCaptures() == 0
	})

	// And the controller is told why the turn ended.
	waitFor(t, "the controller to be told the turn ended", func() bool {
		return mock.VADEnds() > 0 || mock.NoSpeechTimeouts() > 0
	})

	// The audio that was sent was the speech, and only the speech: the
	// frames before anybody talked are not the room.
	if mock.MicFrames() == 0 {
		t.Error("the turn sent no audio at all")
	}
}

// TestABoundedTurnWithNoSpeechEnds checks the turn that nobody takes.
//
// A controller can ask for the microphone for a question the user never
// answers. The device waits for a while and then gives the microphone
// back, rather than holding it open until the controller says stop.
func TestABoundedTurnWithNoSpeechEnds(t *testing.T) {
	mock := mockcontroller.New(mockcontroller.Options{Mode: mockcontroller.ModeApprove})
	defer mock.Close()

	// The room is quiet throughout: nobody answers the question.
	audio := &speakingAudio{}

	emu, err := emulator.New(emulator.Options{
		DeviceID:       "bounded-turn-silent",
		ControllerURL:  mock.URL(),
		CredentialsDir: t.TempDir(),
		Audio:          audio,
		// The wait for somebody to speak is shortened here so the test
		// does not have to wait ten seconds to see it work; the rule
		// under test is that the turn ends without speech, not how long
		// the device waits for it.
		TurnConfig: vad.Config{
			Threshold:  3.0,
			SpeechMs:   200,
			SilenceMs:  5000,
			MaxTurn:    30 * time.Second,
			NoSpeechMs: 2000,
		},
	})
	if err != nil {
		t.Fatalf("building the emulator: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go emu.Run(ctx)

	waitFor(t, "the link to come up", func() bool {
		return emu.Device().State().Snapshot().LinkUp
	})

	if err := mock.RequestMic(true); err != nil {
		t.Fatalf("asking for the microphone: %v", err)
	}

	waitFor(t, "the capture to open", func() bool {
		return audio.openCaptures() > 0
	})

	// The turn ends without anybody having spoken.
	waitFor(t, "the silent turn to end", func() bool {
		return audio.openCaptures() == 0
	})

	// And the controller is told that there was no speech, which is a
	// different answer from "the speech ended".
	waitFor(t, "the controller to be told there was no speech", func() bool {
		return mock.NoSpeechTimeouts() > 0
	})

	// Nothing was said, so nothing was sent.
	if mock.MicFrames() != 0 {
		t.Errorf("%d frames were sent in a turn with no speech", mock.MicFrames())
	}
}

// TestABoundedTurnIsCapped checks the turn's own limit.
//
// A turn may run for as long as a session may, and not a moment longer.
// The controller is told the turn ended, and the microphone is given back.
func TestABoundedTurnIsCapped(t *testing.T) {
	mock := mockcontroller.New(mockcontroller.Options{Mode: mockcontroller.ModeApprove})
	defer mock.Close()

	// Somebody talks for the whole turn and never stops.
	audio := &speakingAudio{speechFrames: 100000}

	emu, err := emulator.New(emulator.Options{
		DeviceID:       "bounded-turn-cap",
		ControllerURL:  mock.URL(),
		CredentialsDir: t.TempDir(),
		Audio:          audio,
		// The cap is the device's own. It is shortened here so the test
		// does not have to wait half a minute to see it work; the rule
		// under test is that the turn ends on its own, not how long it
		// is allowed to last.
		TurnConfig: vad.Config{
			Threshold:  3.0,
			SpeechMs:   200,
			SilenceMs:  5000,
			MaxTurn:    3 * time.Second,
			NoSpeechMs: 10000,
		},
	})
	if err != nil {
		t.Fatalf("building the emulator: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go emu.Run(ctx)

	waitFor(t, "the link to come up", func() bool {
		return emu.Device().State().Snapshot().LinkUp
	})

	if err := mock.RequestMic(true); err != nil {
		t.Fatalf("asking for the microphone: %v", err)
	}

	waitFor(t, "the capture to open", func() bool {
		return audio.openCaptures() > 0
	})

	// The turn ends on its own, even though the controller never said
	// stop and the speech never stopped.
	waitFor(t, "the turn to reach its limit", func() bool {
		return audio.openCaptures() == 0
	})

	waitFor(t, "the controller to be told the turn ended", func() bool {
		return mock.VADEnds() > 0
	})
}
