//go:build !linux || !cgo

// Package ort exposes an optional ONNX Runtime backend. The actual backend
// requires cgo and Linux; the stub keeps the SDK and emulator buildable when
// those native prerequisites are absent.
package ort

import (
	"errors"
	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/wakeword"
)

var ErrUnavailable = errors.New("echomuse/wakeword/ort: requires Linux, cgo, a C compiler, and libonnxruntime.so")

type Options struct {
	Threads                int
	XNNPACK, AllowSpinning bool
}

func DefaultOptions() Options { return Options{Threads: 1, XNNPACK: true} }

type Models struct{ Melspec, Embedding, Classifier, EmbeddingStream string }
type Runtime struct{}
type Inferer struct{}

func Open(string) (*Runtime, error)                           { return nil, ErrUnavailable }
func (*Runtime) Version() string                              { return "" }
func (*Runtime) NewInferer(Models, Options) (*Inferer, error) { return nil, ErrUnavailable }
func (*Inferer) Close() error                                 { return nil }
func (*Inferer) XNNPACKActive() bool                          { return false }
func (*Inferer) Streaming() bool                              { return false }
func (*Inferer) StreamStatus() string                         { return "unavailable" }
func (*Inferer) ResetStream()                                 {}
func (*Inferer) EmbedStep([]float32) ([]float32, error)       { return nil, ErrUnavailable }
func (*Inferer) Melspec([]float32) ([]float32, int, error)    { return nil, 0, ErrUnavailable }
func (*Inferer) Embed([]float32) ([]float32, error)           { return nil, ErrUnavailable }
func (*Inferer) Classify([]float32) (float32, error)          { return 0, ErrUnavailable }

var _ wakeword.Inferer = (*Inferer)(nil)
