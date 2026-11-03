package emulator

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse"
	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/protocol"
)

func TestTapEndpointRequiresPostAndSameOrigin(t *testing.T) {
	device, err := echomuse.New(echomuse.Options{
		DeviceID:     "ui-test",
		Capabilities: []protocol.Capability{protocol.CapButtons},
	})
	if err != nil {
		t.Fatal(err)
	}
	ui := NewHandler(&Emulator{device: device, log: slog.New(slog.DiscardHandler)})

	for _, tc := range []struct {
		name   string
		method string
		origin string
		want   int
	}{
		{name: "GET is rejected", method: http.MethodGet, want: http.StatusMethodNotAllowed},
		{name: "cross origin is rejected", method: http.MethodPost, origin: "http://attacker.example", want: http.StatusForbidden},
		{name: "same origin is accepted", method: http.MethodPost, origin: "http://127.0.0.1:8771", want: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "/api/tap", nil)
			req.Host = "127.0.0.1:8771"
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			rr := httptest.NewRecorder()
			ui.ServeHTTP(rr, req)
			if rr.Code != tc.want {
				t.Fatalf("status = %d, want %d", rr.Code, tc.want)
			}
		})
	}
}
