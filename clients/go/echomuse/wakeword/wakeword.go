// Package wakeword scores a wake word on the device.
//
// It is optional. The controller can score the wake word itself, in which
// case the device streams its microphone to the controller continuously and
// the controller decides when a turn starts.
//
// A device that scores its own wake word does something different and
// better: it sends nothing until the word has been said, so the audio of a
// room never leaves it. That is what this package is for.
//
// The scoring pipeline is openWakeWord's, in three chained models:
//
//	audio → melspectrogram → (ring of mel frames) → embedding →
//	(ring of embeddings) → classifier → score
//
// The buffering between the models is pure Go and lives here. The models
// themselves are behind the Inferer interface, so an application supplies
// the inference implementation. For private listening, run it on-device;
// sending microphone samples to a remote inference service defeats that
// privacy boundary.
//
// # The two ways to use it
//
// Score only — the device scores and reports the crossing but does not act
// on it. The controller's own detector and the device's can then be
// compared on the same audio.
//
// Score and act — the device reports the crossing as a wake, and the
// controller runs the voice turn from it. This is the mode that keeps
// audio on the device.
//
// # Threading
//
// A Detector belongs to one goroutine. The audio path calls it in order and
// the ordering is meaningful, so it is not safe for concurrent use.
package wakeword

import (
	"errors"
	"fmt"
	"time"
)

// The pipeline's fixed points. They come from the models' shapes, not from
// taste, and every one of them is load-bearing: a detector that runs,
// scores plausibly and is quietly wrong is worse than one that does not
// run.
const (
	// SampleRate is fixed by the melspectrogram model.
	SampleRate = 16000
	// ChunkSamples is the quantum the pipeline advances by: 80ms.
	ChunkSamples = 1280

	// MelBins is fixed by the melspectrogram model's output width.
	MelBins = 32
	// MelContextSamples is prepended to each chunk before the
	// melspectrogram is computed. Without it a chunk yields five mel
	// frames instead of the eight the embedding window's step assumes,
	// and the two rings drift apart silently.
	MelContextSamples = 480
	// MelWindow is the embedding model's input height, in mel frames.
	MelWindow = 76
	// MelStep is how far the embedding window advances per chunk: eight of
	// 76, so consecutive embeddings share 89% of their input.
	MelStep = 8
	// MelBufMax caps the mel ring at about ten seconds.
	MelBufMax = 970

	// FeatDim is the embedding model's output width.
	FeatDim = 96
	// FeatWindow is how many embeddings the classifier consumes.
	FeatWindow = 16
	// FeatBufMax caps the embedding ring at about ten seconds.
	FeatBufMax = 120

	// rawBufMax caps the raw audio ring at ten seconds.
	rawBufMax = SampleRate * 10
)

// The mel scale and offset are applied to every melspectrogram output
// before it enters the ring. They bring the exported model back in line
// with the distribution the embedding model was trained on, so they are not
// cosmetic: drop them and the embedding model receives out-of-distribution
// input and every score is wrong while everything still runs.
const (
	melScale  = 10.0
	melOffset = 2.0
)

// ScoreSpan is how much audio one score looks at: the feature window
// embeddings spaced by the mel step, the last covering the mel window's
// frames of 10ms each. A sound stays in the classifier's view this long
// after it ends.
const ScoreSpan = time.Duration((FeatWindow-1)*MelStep+MelWindow) * 10 * time.Millisecond

// Inferer is the boundary between the buffering, which is pure Go and
// lives here, and the models' execution.
//
// All tensors are flat row-major float32, matching the ONNX layouts:
//
//	Melspec:  in  [samples]            -> out [frames*MelBins]
//	Embed:    in  [MelWindow*MelBins]  -> out [FeatDim]
//	Classify: in  [FeatWindow*FeatDim] -> out scalar
type Inferer interface {
	// Melspec computes the mel spectrogram of the samples. It returns the
	// flat result and how many frames it produced.
	Melspec(samples []float32) (mel []float32, frames int, err error)
	// Embed computes one embedding from a window of mel frames.
	Embed(window []float32) ([]float32, error)
	// Classify scores a window of embeddings.
	Classify(feats []float32) (float32, error)
}

// ErrNotReady is returned when the detector has not yet collected enough
// audio to score.
var ErrNotReady = errors.New("echomuse/wakeword: not enough audio yet to score")

// Detector holds the streaming state for one audio source.
//
// It is not safe for concurrent use: the audio path drives it from a single
// goroutine, and that is also the only place where ordering is meaningful.
type Detector struct {
	inf Inferer

	raw       []int16
	remainder []int16
	accum     int

	mel     []float32
	feat    []float32
	scratch []float32
}

// New returns a Detector.
//
// The mel ring starts pre-filled with 1.0, which is what the reference
// implementation does. Starting empty or at zero changes the first second
// of embeddings, so it is load-bearing rather than cosmetic.
func New(inf Inferer) (*Detector, error) {
	if inf == nil {
		return nil, errors.New("echomuse/wakeword: nil inferer")
	}
	mel := make([]float32, MelWindow*MelBins)
	for i := range mel {
		mel[i] = 1.0
	}
	return &Detector{
		inf:     inf,
		mel:     mel,
		scratch: make([]float32, MelWindow*MelBins),
	}, nil
}

// Ready reports whether enough real embeddings have accumulated for the
// classifier to run.
//
// It is false for roughly the first 1.28 seconds of audio, which is the
// price of not scoring against warm-up noise.
func (d *Detector) Ready() bool { return len(d.feat)/FeatDim >= FeatWindow }

// Reset clears the streaming state, as if the Detector were new.
//
// It is used when the microphone stream restarts, so audio from before the
// gap cannot contribute to a score after it.
func (d *Detector) Reset() {
	d.raw = d.raw[:0]
	d.remainder = nil
	d.accum = 0
	d.feat = nil
	d.mel = make([]float32, MelWindow*MelBins)
	for i := range d.mel {
		d.mel[i] = 1.0
	}
}

// Push feeds audio in and returns the score for the chunk just completed.
//
// The samples are signed 16-bit little-endian mono at 16 kHz. A partial
// chunk is buffered until it completes, so the caller can push whatever
// size its reader produces.
//
// ErrNotReady means the chunk was accepted but there is not enough history
// to score yet. It is not a failure: the caller should keep pushing.
func (d *Detector) Push(pcm []byte) (float32, error) {
	if len(pcm)%2 != 0 {
		return 0, fmt.Errorf("echomuse/wakeword: %d bytes is not a whole number of 16-bit samples", len(pcm))
	}

	for i := 0; i+1 < len(pcm); i += 2 {
		d.remainder = append(d.remainder, int16(uint16(pcm[i])|uint16(pcm[i+1])<<8))
	}

	var latest float32
	ready := false
	for len(d.remainder) >= ChunkSamples {
		chunk := d.remainder[:ChunkSamples]
		d.remainder = d.remainder[ChunkSamples:]

		d.raw = append(d.raw, chunk...)
		if len(d.raw) > rawBufMax {
			d.raw = d.raw[len(d.raw)-rawBufMax:]
		}

		score, err := d.step()
		if err != nil {
			return 0, err
		}
		if score >= 0 {
			latest, ready = score, true
		}
	}
	if !ready {
		return 0, ErrNotReady
	}
	return latest, nil
}

// RecentAudio returns a copy of the newest buffered samples, suitable as private-listening pre-roll.
// The requested duration is capped at the detector's ten second ring and the result is little-endian PCM.
func (d *Detector) RecentAudio(duration time.Duration) []byte {
	if duration <= 0 {
		return nil
	}
	if duration > 10*time.Second {
		duration = 10 * time.Second
	}
	n := int(duration * SampleRate / time.Second)
	if n > len(d.raw) {
		n = len(d.raw)
	}
	if n == 0 {
		return nil
	}
	raw := d.raw[len(d.raw)-n:]
	out := make([]byte, n*2)
	for i, sample := range raw {
		out[i*2] = byte(sample)
		out[i*2+1] = byte(uint16(sample) >> 8)
	}
	return out
}

// step advances the pipeline by one chunk and returns the score, or a
// negative value when there is not enough history to score.
func (d *Detector) step() (float32, error) {
	// The melspectrogram is computed over the new chunk plus 480 samples of
	// left context, so 1280 new samples yield eight new mel frames rather
	// than five.
	start := len(d.raw) - ChunkSamples - MelContextSamples
	if start < 0 {
		start = 0
	}
	samples := make([]float32, 0, len(d.raw)-start)
	for _, s := range d.raw[start:] {
		samples = append(samples, float32(s))
	}

	mel, frames, err := d.inf.Melspec(samples)
	if err != nil {
		return 0, fmt.Errorf("echomuse/wakeword: the melspectrogram failed: %w", err)
	}
	if frames <= 0 {
		return 0, ErrNotReady
	}
	if frames > len(mel)/MelBins {
		return 0, fmt.Errorf("echomuse/wakeword: melspectrogram returned %d frames in %d values", frames, len(mel))
	}

	for i := 0; i < frames*MelBins; i++ {
		mel[i] = mel[i]/melScale + melOffset
	}

	d.mel = append(d.mel, mel...)
	if len(d.mel) > MelBufMax*MelBins {
		d.mel = d.mel[len(d.mel)-MelBufMax*MelBins:]
	}

	// The embedding window steps eight frames but is 76 wide, so
	// consecutive windows overlap by 89%. A window is emitted whenever
	// enough new frames have arrived.
	newFrames := frames
	for newFrames >= MelStep {
		newFrames -= MelStep

		window := make([]float32, MelWindow*MelBins)
		copy(window, d.mel[len(d.mel)-(MelWindow*MelBins):])

		emb, err := d.inf.Embed(window)
		if err != nil {
			return 0, fmt.Errorf("echomuse/wakeword: the embedding failed: %w", err)
		}
		if len(emb) != FeatDim {
			return 0, fmt.Errorf("echomuse/wakeword: the embedding returned %d values, expected %d", len(emb), FeatDim)
		}
		d.feat = append(d.feat, emb...)
		if len(d.feat) > FeatBufMax*FeatDim {
			d.feat = d.feat[len(d.feat)-FeatBufMax*FeatDim:]
		}
	}

	if !d.Ready() {
		return -1, nil
	}

	window := d.feat[len(d.feat)-FeatWindow*FeatDim:]
	score, err := d.inf.Classify(window)
	if err != nil {
		return 0, fmt.Errorf("echomuse/wakeword: the classifier failed: %w", err)
	}
	return score, nil
}

// Crossing is a wake word detection.
type Crossing struct {
	// Score is the score at the crossing.
	Score float32
	// Threshold is the threshold it crossed.
	Threshold float32
	// At is when the crossing happened.
	At time.Time
}

// DefaultRefractory suppresses repeat crossings after one fires.
//
// Wake scores stay above the threshold for several consecutive frames, so
// without this a single utterance emits a handful of crossings.
const DefaultRefractory = 1500 * time.Millisecond

// Trigger holds the decision of whether a score becomes a wake.
//
// It is separate from the Detector so the scoring and the policy do not
// have to change together: a device that scores in shadow mode uses the
// same Detector and a Trigger that never fires.
type Trigger struct {
	// Threshold is the score a wake needs to reach.
	Threshold float32
	// Refractory is how long after a crossing another is ignored.
	Refractory time.Duration
	// OnCrossing is called when a crossing fires. Returning false
	// suppresses it, which is how a device in shadow mode reports without
	// acting.
	OnCrossing func(Crossing) bool

	last time.Time
}

// NewTrigger returns a Trigger.
func NewTrigger(threshold float32, onCrossing func(Crossing) bool) *Trigger {
	return &Trigger{
		Threshold:  threshold,
		Refractory: DefaultRefractory,
		OnCrossing: onCrossing,
	}
}

// Consider offers a score to the trigger.
//
// It reports whether a crossing fired. A score below the threshold is
// nothing; a score above it within the refractory period is the same
// utterance seen again.
func (t *Trigger) Consider(score float32, now time.Time) (Crossing, bool) {
	if score < t.Threshold {
		return Crossing{}, false
	}
	if !t.last.IsZero() && now.Sub(t.last) < t.Refractory {
		return Crossing{}, false
	}
	t.last = now

	c := Crossing{Score: score, Threshold: t.Threshold, At: now}
	if t.OnCrossing != nil && !t.OnCrossing(c) {
		return Crossing{}, false
	}
	return c, true
}
