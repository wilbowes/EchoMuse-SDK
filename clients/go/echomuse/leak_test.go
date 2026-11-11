package echomuse_test

import (
	"context"
	"testing"
	"time"

	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse"
)

// TestTheCompositeDoesNotLeak checks the memory over many transfers.
func TestTheCompositeDoesNotLeak(t *testing.T) {
	composite := echomuse.MultiShellRunner{
		Planes: []echomuse.ShellRunner{largePlane{}},
	}
	session, err := composite.Start(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	body := make([]byte, 15<<20)
	head := "base64 -d << 'DELIM' > /data/local/share/echomuse/oww/model.onnx.part\n"
	tail := "\nDELIM\nchmod 644 /data/local/share/echomuse/oww/model.onnx.part && echo TRANSFER_OK"

	before := heapMB()
	for round := 0; round < 3; round++ {
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
		time.Sleep(300 * time.Millisecond)
		t.Logf("round %d: the heap is %d MB", round, heapMB())
	}
	after := heapMB()
	t.Logf("the heap went from %d MB to %d MB", before, after)
	if after-before > 512 {
		t.Errorf("the heap grew by %d MB", after-before)
	}
}
