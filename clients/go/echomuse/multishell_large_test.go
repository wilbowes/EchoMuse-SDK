package echomuse_test

import (
	"context"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse"
)

// TestTheCompositeHoldsNoMoreThanItMust checks the memory of a transfer
// through the composite.
//
// The transfer is the biggest thing on the wire, and it goes to more than
// one plane. A plane that holds a transfer it will only refuse is a
// plane that fills the machine's memory.
func TestTheCompositeHoldsNoMoreThanItMust(t *testing.T) {
	composite := echomuse.MultiShellRunner{
		Planes: []echomuse.ShellRunner{
			echomuse.CredentialShellRunner(t.TempDir()),
			largePlane{},
		},
	}
	session, err := composite.Start(context.Background(), false)
	if err != nil {
		t.Fatalf("starting: %v", err)
	}
	defer session.Close()

	body := make([]byte, 15<<20)
	head := "base64 -d << 'DELIM' > /data/local/share/echomuse/oww/model.onnx.part\n"
	tail := "\nDELIM\nchmod 644 /data/local/share/echomuse/oww/model.onnx.part && echo TRANSFER_OK"

	before := heapMB()
	start := time.Now()
	session.Write([]byte(head))
	const frame = 64 << 10
	for i := 0; i < len(body); i += frame {
		end := i + frame
		if end > len(body) {
			end = len(body)
		}
		session.Write(body[i:end])
	}
	session.Write([]byte(tail))
	t.Logf("the transfer took %v", time.Now().Sub(start))

	// Give the goroutine that carries the command a moment to hold it.
	time.Sleep(200 * time.Millisecond)
	after := heapMB()
	t.Logf("the heap went from %d MB to %d MB", before, after)
	if after-before > 512 {
		t.Errorf("the heap grew by %d MB, want it bounded", after-before)
	}
}

// heapMB is the process's heap in megabytes.
func heapMB() int {
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return int(stats.HeapAlloc / (1024 * 1024))
}

// largePlane carries the big commands.
type largePlane struct{}

// CarriesLargeCommands says the plane carries one.
func (largePlane) CarriesLargeCommands() bool { return true }

// Start begins a session.
func (largePlane) Start(context.Context, bool) (echomuse.ShellSession, error) {
	session := &largeSession{}
	session.ready = sync.NewCond(&session.mu)
	return session, nil
}

// largeSession answers a command with the marker.
//
// A plane that carries a large command answers it. One that never
// answers costs the caller the whole of the answer timeout, which is
// what the memory tests are not measuring.
type largeSession struct {
	mu    sync.Mutex
	out   []byte
	ready *sync.Cond
}

// Read returns the answer.
func (s *largeSession) Read(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for len(s.out) == 0 {
		s.ready.Wait()
	}
	n := copy(p, s.out)
	s.out = s.out[n:]
	return n, nil
}

// Write takes the command and answers it.
func (s *largeSession) Write(p []byte) (int, error) {
	s.mu.Lock()
	s.out = append(s.out, echomuse.FrameAnswer("OK\n")...)
	s.mu.Unlock()
	s.ready.Broadcast()
	return len(p), nil
}

// Close ends the session.
func (s *largeSession) Close() error { return nil }
