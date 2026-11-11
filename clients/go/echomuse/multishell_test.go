package echomuse_test

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse"
)

// stubPlane answers a command with a fixed answer.
type stubPlane struct {
	answer string
	// silent answers nothing at all, which is the case that used to
	// fill the machine's memory.
	silent bool
}

// Start begins a session.
func (s stubPlane) Start(context.Context, bool) (echomuse.ShellSession, error) {
	return newStubSession(s), nil
}

// stubSession is one session on a stub plane.
type stubSession struct {
	plane  stubPlane
	mu     sync.Mutex
	out    []byte
	ready  *sync.Cond
	closed bool
}

func newStubSession(plane stubPlane) *stubSession {
	session := &stubSession{plane: plane}
	session.ready = sync.NewCond(&session.mu)
	return session
}

func (s *stubSession) Read(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for len(s.out) == 0 {
		if s.closed {
			return 0, io.EOF
		}
		s.ready.Wait()
	}
	n := copy(p, s.out)
	s.out = s.out[n:]
	return n, nil
}

func (s *stubSession) Write(p []byte) (int, error) {
	if s.plane.silent {
		return len(p), nil
	}
	s.mu.Lock()
	s.out = append(s.out, s.plane.answer...)
	s.mu.Unlock()
	s.ready.Broadcast()
	return len(p), nil
}

func (s *stubSession) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.ready.Broadcast()
	return nil
}

// TestTheCompositePlaneAnswers checks that the first plane that
// recognises a command answers it.
func TestTheCompositePlaneAnswers(t *testing.T) {
	runner := echomuse.MultiShellRunner{
		Planes: []echomuse.ShellRunner{
			stubPlane{answer: "first\n"},
			stubPlane{answer: "second\n"},
		},
	}
	session, err := runner.Start(context.Background(), false)
	if err != nil {
		t.Fatalf("starting the session: %v", err)
	}
	defer session.Close()

	if _, err := session.Write([]byte("do the thing\n")); err != nil {
		t.Fatalf("writing the command: %v", err)
	}

	buf := make([]byte, 64)
	n, err := session.Read(buf)
	if err != nil {
		t.Fatalf("reading the answer: %v", err)
	}
	// The answer carries the marker the controller waits for. A device
	// that answers without it leaves the controller waiting for the whole
	// of its timeout.
	got := string(buf[:n])
	if !strings.Contains(got, "first") {
		t.Errorf("the answer was %q, want the first plane's", got)
	}
	if !strings.Contains(got, echomuse.CommandSentinel) {
		t.Errorf("the answer was %q, want it to carry the marker", got)
	}
}

// TestAPlaneThatRefusesIsPassedOver checks that a refusal means "not
// mine".
//
// The credential plane refuses every command it does not recognise, and
// the asset plane refuses every command it does not recognise. A
// composite that took the first refusal as the answer would ask the
// credential plane about the assets and never ask the asset plane at
// all.
func TestAPlaneThatRefusesIsPassedOver(t *testing.T) {
	runner := echomuse.MultiShellRunner{
		Planes: []echomuse.ShellRunner{
			stubPlane{answer: "echomuse: refused: this plane carries the credential transfer only\n"},
			stubPlane{answer: "the assets\n"},
		},
	}
	session, err := runner.Start(context.Background(), false)
	if err != nil {
		t.Fatalf("starting the session: %v", err)
	}
	defer session.Close()

	if _, err := session.Write([]byte("list the assets\n")); err != nil {
		t.Fatalf("writing the command: %v", err)
	}

	buf := make([]byte, 128)
	n, err := session.Read(buf)
	if err != nil {
		t.Fatalf("reading the answer: %v", err)
	}
	got := string(buf[:n])
	if !strings.Contains(got, "the assets") {
		t.Errorf("the answer was %q, want the second plane's", got)
	}
	if strings.Contains(got, "credential transfer only") {
		t.Errorf("the answer was %q, want the refusal to have been passed over", got)
	}
}

// TestTheCompositePlaneDoesNotHangOnASilentPlane is the test that matters.
//
// A plane that answers nothing must cost a moment, not the machine's
// memory. Reading it with io.ReadAll on a reader that never ends grows a
// buffer without limit — which is what filled the RAM.
func TestTheCompositePlaneDoesNotHangOnASilentPlane(t *testing.T) {
	runner := echomuse.MultiShellRunner{
		Planes: []echomuse.ShellRunner{
			stubPlane{silent: true},
			stubPlane{answer: "second\n"},
		},
	}
	session, err := runner.Start(context.Background(), false)
	if err != nil {
		t.Fatalf("starting the session: %v", err)
	}
	defer session.Close()

	if _, err := session.Write([]byte("do the thing\n")); err != nil {
		t.Fatalf("writing the command: %v", err)
	}

	done := make(chan string, 1)
	go func() {
		buf := make([]byte, 64)
		n, err := session.Read(buf)
		if err != nil {
			done <- "error: " + err.Error()
			return
		}
		done <- string(buf[:n])
	}()

	select {
	case got := <-done:
		if !strings.Contains(got, "second") {
			t.Errorf("the answer was %q, want the second plane's", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the session did not answer")
	}
}

// TestTheCompositePlaneEndsAtEOF checks that a closed session reads as
// finished.
//
// A reader that never returns EOF is a reader that whoever calls
// io.ReadAll on it will read for ever.
func TestTheCompositePlaneEndsAtEOF(t *testing.T) {
	runner := echomuse.MultiShellRunner{
		Planes: []echomuse.ShellRunner{stubPlane{answer: "answer\n"}},
	}
	session, err := runner.Start(context.Background(), false)
	if err != nil {
		t.Fatalf("starting the session: %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("closing the session: %v", err)
	}

	// io.ReadAll must finish. On a reader that never ends it does not,
	// and it grows its buffer until the machine has no memory left.
	done := make(chan int, 1)
	go func() {
		data, _ := io.ReadAll(session)
		done <- len(data)
	}()

	select {
	case n := <-done:
		if n != 0 {
			t.Errorf("the closed session read %d bytes, want none", n)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("reading a closed session did not finish")
	}
}
