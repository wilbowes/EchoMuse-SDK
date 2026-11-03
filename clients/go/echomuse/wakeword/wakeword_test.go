package wakeword_test

import (
	"testing"
	"time"

	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/wakeword"
)

// stubInferer is a stand-in for the three models.
//
// It returns a fixed mel spectrogram, a fixed embedding and a fixed score,
// which is enough to drive the buffering and the readiness rules.
type stubInferer struct {
	score    float32
	melErr   error
	embedErr error
}

// Melspec returns a mel spectrogram of the right shape.
func (s *stubInferer) Melspec(samples []float32) ([]float32, int, error) {
	if s.melErr != nil {
		return nil, 0, s.melErr
	}
	// Eight frames of thirty-two bins, which is what a 1280-sample chunk
	// with the left context produces.
	frames := 8
	mel := make([]float32, frames*wakeword.MelBins)
	for i := range mel {
		mel[i] = 0.5
	}
	return mel, frames, nil
}

// Embed returns an embedding of the right shape.
func (s *stubInferer) Embed(window []float32) ([]float32, error) {
	if s.embedErr != nil {
		return nil, s.embedErr
	}
	emb := make([]float32, wakeword.FeatDim)
	for i := range emb {
		emb[i] = 0.25
	}
	return emb, nil
}

// Classify returns the fixed score.
func (s *stubInferer) Classify(feats []float32) (float32, error) {
	return s.score, nil
}

// TestDetectorNeedsHistoryBeforeItScores checks the readiness rule.
//
// The detector reports no score until it has enough real embeddings, which
// is about 1.28 seconds of audio. A score derived from warm-up noise is
// not a score.
func TestDetectorNeedsHistoryBeforeItScores(t *testing.T) {
	detector, err := wakeword.New(&stubInferer{score: 0.9})
	if err != nil {
		t.Fatalf("building the detector: %v", err)
	}

	chunk := make([]byte, wakeword.ChunkSamples*2)
	_, err = detector.Push(chunk)
	if err != wakeword.ErrNotReady {
		t.Fatalf("the first chunk returned %v, want ErrNotReady", err)
	}
	if detector.Ready() {
		t.Error("the detector reported itself ready with no history")
	}
}

// TestDetectorScoresOnceItHasHistory checks that it does score, once it
// has enough.
func TestDetectorScoresOnceItHasHistory(t *testing.T) {
	detector, err := wakeword.New(&stubInferer{score: 0.9})
	if err != nil {
		t.Fatalf("building the detector: %v", err)
	}

	chunk := make([]byte, wakeword.ChunkSamples*2)
	var score float32
	scored := false

	for i := 0; i < 40; i++ {
		s, err := detector.Push(chunk)
		if err == nil {
			score = s
			scored = true
			break
		}
	}

	if !scored {
		t.Fatal("the detector never scored")
	}
	if score != 0.9 {
		t.Errorf("the score was %v, want 0.9", score)
	}
	if !detector.Ready() {
		t.Error("the detector reported itself unready after scoring")
	}
}

// TestDetectorBuffersAPartialChunk checks that a chunk split across two
// pushes is assembled rather than dropped.
func TestDetectorBuffersAPartialChunk(t *testing.T) {
	detector, err := wakeword.New(&stubInferer{score: 0.5})
	if err != nil {
		t.Fatalf("building the detector: %v", err)
	}

	half := make([]byte, wakeword.ChunkSamples) // half a chunk
	if _, err := detector.Push(half); err != wakeword.ErrNotReady {
		t.Fatalf("the first half returned %v, want ErrNotReady", err)
	}
	if _, err := detector.Push(half); err != wakeword.ErrNotReady {
		t.Fatalf("the second half returned %v, want ErrNotReady", err)
	}

	// The whole chunk has now been seen, so the detector is further along
	// than it would have been after one whole push of the same size.
	for i := 0; i < 40; i++ {
		if _, err := detector.Push(half); err == nil {
			return
		}
	}
}

// TestDetectorRejectsAnOddByteCount checks that a buffer which is not a
// whole number of samples is refused rather than silently misread.
func TestDetectorRejectsAnOddByteCount(t *testing.T) {
	detector, err := wakeword.New(&stubInferer{})
	if err != nil {
		t.Fatalf("building the detector: %v", err)
	}
	if _, err := detector.Push([]byte{1, 2, 3}); err == nil {
		t.Error("an odd byte count was accepted")
	}
}

// TestResetClearsTheHistory checks that a reset leaves the detector as new.
func TestResetClearsTheHistory(t *testing.T) {
	detector, err := wakeword.New(&stubInferer{score: 0.9})
	if err != nil {
		t.Fatalf("building the detector: %v", err)
	}

	chunk := make([]byte, wakeword.ChunkSamples*2)
	for i := 0; i < 40; i++ {
		if _, err := detector.Push(chunk); err == nil {
			break
		}
	}
	if !detector.Ready() {
		t.Fatal("the detector never became ready")
	}

	detector.Reset()
	if detector.Ready() {
		t.Error("the detector was still ready after a reset")
	}
}

// TestTriggerFiresOnACrossing checks the crossing rule.
func TestTriggerFiresOnACrossing(t *testing.T) {
	var crossings int
	trigger := wakeword.NewTrigger(0.5, func(c wakeword.Crossing) bool {
		crossings++
		return true
	})

	now := time.Now()
	if _, fired := trigger.Consider(0.4, now); fired {
		t.Error("a score below the threshold fired")
	}
	if _, fired := trigger.Consider(0.9, now); !fired {
		t.Error("a score above the threshold did not fire")
	}
	if crossings != 1 {
		t.Errorf("the callback ran %d times, want 1", crossings)
	}
}

// TestTriggerSuppressesRepeatCrossings checks the refractory period.
//
// Wake scores stay above the threshold for several consecutive frames, so
// without this a single utterance emits a handful of crossings.
func TestTriggerSuppressesRepeatCrossings(t *testing.T) {
	var crossings int
	trigger := wakeword.NewTrigger(0.5, func(c wakeword.Crossing) bool {
		crossings++
		return true
	})

	now := time.Now()
	trigger.Consider(0.9, now)
	trigger.Consider(0.9, now.Add(100*time.Millisecond))
	trigger.Consider(0.9, now.Add(200*time.Millisecond))

	if crossings != 1 {
		t.Errorf("the callback ran %d times inside the refractory period, want 1", crossings)
	}

	// Past the refractory period, a new crossing is a new crossing.
	if _, fired := trigger.Consider(0.9, now.Add(2*time.Second)); !fired {
		t.Error("a crossing past the refractory period did not fire")
	}
}

// TestTriggerCanSuppressACrossing checks that the callback can refuse.
//
// This is how a device scores in shadow mode: it reports the crossing
// without acting on it.
func TestTriggerCanSuppressACrossing(t *testing.T) {
	trigger := wakeword.NewTrigger(0.5, func(c wakeword.Crossing) bool {
		return false
	})

	if _, fired := trigger.Consider(0.9, time.Now()); fired {
		t.Error("a crossing the callback refused still fired")
	}
}
