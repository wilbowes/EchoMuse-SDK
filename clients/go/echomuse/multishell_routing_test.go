package echomuse_test

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse"
)

// TestALargeCommandIsNotOfferedToAPlaneThatWillRefuseIt checks the
// routing.
//
// The credential plane copies every frame it is given into a buffer, so a
// multi-megabyte transfer offered to it is held for the length of the
// transfer — and the transfer is the largest thing on the wire.
func TestALargeCommandIsNotOfferedToAPlaneThatWillRefuseIt(t *testing.T) {
	credential := &countingPlane{}
	composite := echomuse.MultiShellRunner{
		Planes: []echomuse.ShellRunner{credential, largePlane{}},
	}
	session, err := composite.Start(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	go func() {
		buf := make([]byte, 4096)
		for {
			if _, err := session.Read(buf); err != nil {
				return
			}
		}
	}()

	body := make([]byte, 15<<20)
	session.Write([]byte("base64 -d << 'DELIM' > /x/model.onnx.part\n"))
	for i := 0; i < len(body); i += 65536 {
		end := i + 65536
		if end > len(body) {
			end = len(body)
		}
		session.Write(body[i:end])
	}
	session.Write([]byte("\nDELIM\nchmod 644 /x/model.onnx.part && echo TRANSFER_OK\n"))
	time.Sleep(500 * time.Millisecond)

	// The credential plane is offered the commands that come AFTER the
	// transfer — the chmod is one of them, and it is small. What it is
	// not offered is the transfer itself: the body is the largest thing
	// on the wire, and a plane that would only refuse it must never
	// hold it.
	t.Logf("the credential plane was given %d bytes", credential.count())
	if credential.count() > 4096 {
		t.Errorf("the credential plane was given %d bytes of a transfer it cannot carry", credential.count())
	}
}

// countingPlane records what it is given.
type countingPlane struct {
	mu      sync.Mutex
	written int
}

// count is how much the plane has been given.
func (c *countingPlane) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.written
}

// Start begins a session.
func (c *countingPlane) Start(context.Context, bool) (echomuse.ShellSession, error) {
	return &countingSession{plane: c}, nil
}

// countingSession records what is written to it.
type countingSession struct {
	plane *countingPlane
}

// Read returns nothing.
func (s *countingSession) Read(p []byte) (int, error) { return 0, io.EOF }

// Write records the command.
func (s *countingSession) Write(p []byte) (int, error) {
	s.plane.mu.Lock()
	s.plane.written += len(p)
	s.plane.mu.Unlock()
	return len(p), nil
}

// Close ends the session.
func (s *countingSession) Close() error { return nil }
