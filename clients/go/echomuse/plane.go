package echomuse

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/protocol"
	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/state"
	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/transport"
)

// TokenHeader is the header the link credential travels in.
//
// It must be on all three planes. The controller refuses a connection that
// presents a wrong token, and refuses a device that has presented its token
// before and then stops — so a device that sends the token on the control
// plane only is admitted there and refused on the other two.
const TokenHeader = "X-EM-Token"

// Keepalive timings, from the contract.
//
// The controller pings every twenty seconds and closes a connection after
// thirty with no pong. The device answers promptly, which means never
// blocking the goroutine that reads the socket.
//
// The read deadline is longer than the controller's ping interval, so a
// quiet link — one carrying no messages at all — stays open as long as the
// controller keeps pinging.
const (
	pingInterval = 20 * time.Second
	pongWait     = 45 * time.Second
	writeWait    = 10 * time.Second
)

// plane is one WebSocket connection to the controller.
type plane struct {
	conn   *websocket.Conn
	base   string
	mu     sync.Mutex
	closed bool
}

// refreshDeadline pushes the read deadline out.
//
// Every message read resets it, because a link that carries messages is a
// link that is alive, and the read deadline is there to catch a half-open
// socket rather than to police the traffic.
//
// The deadline cannot rely on the Pong handler alone: the controller sends
// Ping frames, and gorilla answers those by itself without ever calling the
// Pong handler. A deadline refreshed only by a Pong therefore expires on a
// perfectly healthy link, at exactly one deadline after it was set.
func (p *plane) refreshDeadline() {
	p.conn.SetReadDeadline(time.Now().Add(pongWait))
}

// keepalive pings a connection on the interval the contract asks for.
//
// The controller pings every plane it serves and closes one that has not
// answered. A device that only listens therefore depends on the controller's
// pings for its own liveness, and a plane that is quiet in both directions
// — the shell plane while a human decides what to type — has nothing to
// refresh its own deadline with.
//
// WriteControl is safe alongside WriteMessage, which is why the ping does
// not take the write lock: a write blocked on a full send buffer must not
// be able to stop the ping that would notice the path is dead.
func keepalive(conn *websocket.Conn) {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()

	for range ticker.C {
		if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeWait)); err != nil {
			return
		}
	}
}

// writeJSON sends a text message.
func (p *plane) writeJSON(v any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return ErrNotConnected
	}
	p.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return p.conn.WriteJSON(v)
}

// writeBinary sends a binary message.
func (p *plane) writeBinary(b []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return ErrNotConnected
	}
	p.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return p.conn.WriteMessage(websocket.BinaryMessage, b)
}

// Close shuts the connection down.
func (p *plane) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.closed = true
	p.conn.Close()
}

// dialBase opens one plane on a named base URL.
//
// The scheme depends on the credentials: a device holding the controller's
// certificate authority dials wss only, and never sends its token over a
// plain connection. A device without one dials plain.
func (d *Device) dialBase(ctx context.Context, path, base string, creds transport.Credentials) (*plane, error) {
	target := base + path
	u, err := url.Parse(target)
	if err != nil {
		return nil, fmt.Errorf("echomuse: bad controller URL %q: %w", target, err)
	}

	header := http.Header{}
	if creds.Token != "" {
		// A device holding a CA never sends its token on a plain dial.
		if u.Scheme != "ws" {
			header.Set(TokenHeader, creds.Token)
		}
	}

	dialer := &websocket.Dialer{
		HandshakeTimeout:  15 * time.Second,
		EnableCompression: false,
		TLSClientConfig:   tlsConfigFor(creds),
	}

	conn, resp, err := dialer.DialContext(ctx, target, header)
	if err != nil {
		if resp != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("echomuse: dialling %s: %w (HTTP %d)", target, err, resp.StatusCode)
		}
		return nil, fmt.Errorf("echomuse: dialling %s: %w", target, err)
	}
	if resp != nil {
		resp.Body.Close()
	}

	conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(pongWait))
	})
	go keepalive(conn)

	return &plane{conn: conn, base: base}, nil
}

// dialBases returns the base URLs a plane may be dialled on, in order.
//
// A device without a certificate authority dials plain. A device with one
// dials the encrypted link first, because that is the link it is trusted
// on.
//
// It also dials plain while a pairing window is open. A device being
// paired with a new controller holds that controller's certificate
// authority for the first time, so the encrypted link cannot be opened
// until it has been issued one — and that is the whole of a re-pair. The
// credential transfer rides the shell plane over the network, so a device
// can move to a different controller without a cable, a serial port or an
// adb session.
//
// A device whose controller offers no encrypted port dials plain as well.
// Holding a certificate authority for a controller that has no encrypted
// listener would otherwise leave the device unable to reach it at all,
// and the credential it holds would be the very thing that stranded it.
//
// The plain fallback carries no token. The credential belongs to the
// controller that issued it, and a different controller has no business
// seeing it.
//
// A device with a token but no certificate authority is in the middle of
// being issued credentials, and it dials plain without the token — which
// the controller admits while the device has never presented one.
func (d *Device) dialBases(link transport.Controller, creds transport.Credentials) []string {
	if len(creds.CAPEM) == 0 {
		return []string{link.URL()}
	}

	var bases []string
	if link.HasTLS() {
		bases = append(bases, link.TLSURL())
	}
	if d.PairingOpen() || !link.HasTLS() {
		bases = append(bases, link.URL())
	}
	return bases
}

// dialURL is the URL a dial uses, for the log.
//
// It differs from the plain URL whenever the device holds the controller's
// certificate authority, because then the link is encrypted and saying
// otherwise in the log is how a half-encrypted link gets missed.
func (d *Device) dialURL(link transport.Controller, creds transport.Credentials) string {
	bases := d.dialBases(link, creds)
	if len(bases) == 0 {
		return link.URL()
	}
	return bases[0]
}

// tlsConfigFor builds the TLS configuration for a dial.
func tlsConfigFor(creds transport.Credentials) *tls.Config {
	if len(creds.CAPEM) == 0 {
		return nil
	}
	cfg, err := creds.TLSConfig(transport.DefaultTLSServerName)
	if err != nil {
		return nil
	}
	return cfg
}

// dialControl opens the control plane.
func (d *Device) dialControl(ctx context.Context, link transport.Controller, creds transport.Credentials) (*plane, error) {
	return d.dialPlanes(ctx, "/control", link, creds)
}

// dialPlanes opens one plane, trying each base URL in turn.
//
// A failure on the encrypted link is not a failure while a pairing window
// is open: the plain link is the one that reaches a controller this device
// has not been issued a credential for yet.
func (d *Device) dialPlanes(ctx context.Context, path string, link transport.Controller, creds transport.Credentials) (*plane, error) {
	bases := d.dialBases(link, creds)
	if len(bases) == 0 {
		return nil, fmt.Errorf("echomuse: the device holds a certificate authority but the controller offers no encrypted port")
	}

	var lastErr error
	for _, base := range bases {
		p, err := d.dialBase(ctx, path, base, creds)
		if err == nil {
			return p, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// dialData opens the data plane.
//
// The first message on this plane is an identify, not audio. The controller
// reads the first frame to learn which device the connection is for, and
// closes a data plane whose first frame is anything else — including a mic
// frame, which it has no device id to route.
func (d *Device) dialData(ctx context.Context, link transport.Controller, creds transport.Credentials) (*plane, error) {
	p, err := d.dialPlanes(ctx, "/data", link, creds)
	if err != nil {
		return nil, err
	}
	if err := p.writeJSON(protocol.Identify{
		Type:     protocol.TypeIdentify,
		DeviceID: d.opts.DeviceID,
	}); err != nil {
		p.Close()
		return nil, fmt.Errorf("echomuse: identifying to the controller: %w", err)
	}
	return p, nil
}

// parseControllerURL reads a controller base URL.
//
// The port is the plain port. A second port in the URL is read as the
// encrypted one, which is how a controller that runs both is named, and a
// `wss://` URL names the encrypted port on its own.
//
// The encrypted port is not guessed. A device that holds the controller's
// certificate authority dials only wss, and a controller that offers no
// encrypted port is a controller the device cannot join — so a URL that
// names no encrypted port has to mean "there is none" until the device
// discovers one.
func parseControllerURL(raw string) (transport.Controller, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return transport.Controller{}, fmt.Errorf("echomuse: bad controller URL %q: %w", raw, err)
	}
	if u.Host == "" {
		return transport.Controller{}, fmt.Errorf("echomuse: controller URL %q has no host", raw)
	}
	host := u.Hostname()
	port := 8767
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return transport.Controller{}, fmt.Errorf("echomuse: controller URL %q has a bad port", raw)
		}
		port = n
	}
	tlsPort := 0
	switch u.Scheme {
	case "wss":
		tlsPort = port
		port = 8767
	case "https":
		tlsPort = port
		port = 8767
	}
	return transport.Controller{Host: host, Port: port, TLSPort: tlsPort}, nil
}

// serveControl reads the control plane until it drops.
func (d *Device) serveControl(ctx context.Context, p *plane) {
	defer p.Close()

	// The controller pings every twenty seconds; the pong answers it. The
	// read deadline is extended by the pong handler, so a quiet link stays
	// open as long as the controller keeps pinging.
	go d.answerPings(ctx, p)

	for {
		msgType, data, err := p.conn.ReadMessage()
		if err != nil {
			if ctx.Err() == nil {
				d.log.Debug("control plane closed", "error", err)
			}
			return
		}
		p.refreshDeadline()
		switch msgType {
		case websocket.TextMessage:
			d.handleControl(p, data)
		case websocket.BinaryMessage:
			d.log.Debug("unexpected binary message on the control plane", "bytes", len(data))
		}
	}
}

// answerPings answers the controller's keepalive.
//
// The pong carries the device's monotonic clock, which the controller maps
// onto its own to date the captured audio. It answers a ping that carries an
// id with the same id, so the controller can match the two.
func (d *Device) answerPings(ctx context.Context, p *plane) {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := p.writeJSON(protocol.Pong{Type: protocol.TypePong}); err != nil {
				return
			}
		}
	}
}

// handleControl dispatches one control-plane message.
func (d *Device) handleControl(p *plane, raw []byte) {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		d.log.Debug("undecodable control message", "error", err)
		return
	}

	msg := protocol.Incoming{Type: head.Type, Raw: raw}

	switch head.Type {
	case protocol.TypeAck:
		d.handleAck(raw)
	case protocol.TypeRefused:
		d.state.SetPhase(state.PhaseRefused, "the controller refused this device's credentials")
		d.state.SetApproved(false)
		d.log.Warn("refused by the controller")
	case protocol.TypePending:
		d.state.SetPhase(state.PhasePending, "waiting for an administrator to approve this device")
		d.state.SetApproved(false)
		d.log.Info("pending approval")
	case protocol.TypeLEDs:
		var m protocol.LEDs
		if json.Unmarshal(raw, &m) == nil {
			d.emitLEDs(m)
		}
	case protocol.TypeLEDAnim:
		var m protocol.LEDAnim
		if json.Unmarshal(raw, &m) == nil {
			d.emitLEDAnim(m.Anim)
		}
	case protocol.TypeMicStart:
		var m protocol.MicStart
		json.Unmarshal(raw, &m)
		d.state.SetListening(true)
		d.emitMicStart(m.LockMic)
	case protocol.TypeMicStop:
		d.state.SetListening(false)
		d.emitMicStop()
	case protocol.TypeListenAck:
		var m protocol.ListenAck
		if json.Unmarshal(raw, &m) == nil {
			d.ackListenSession(m.Session)
			d.emitListenAck(m.Session)
		}
	case protocol.TypeListenClose:
		var m protocol.ListenClose
		if json.Unmarshal(raw, &m) == nil {
			d.clearListenSession(m.Session)
			d.emitListenClose(m)
		}
	case protocol.TypeVolumeSet:
		var m protocol.VolumeSet
		json.Unmarshal(raw, &m)
		d.state.SetVolume(m.Level)
		d.emitVolumeSet(m.Level)
	case protocol.TypeDuck:
		var m protocol.Duck
		if json.Unmarshal(raw, &m) == nil {
			d.emitDuck(m.On)
		}
	case protocol.TypePlayCue:
		var m protocol.PlayCue
		d.emitCue(m.Cue)
	case protocol.TypeMusicFlush:
		d.emitFlush("music")
	case protocol.TypeSpeakerFlush:
		d.emitFlush("speaker")
	case protocol.TypeShellOpen:
		var m protocol.ShellOpen
		d.openShell(m.Pty)
	case protocol.TypeShellClose:
		d.closeShell()
	case protocol.TypeConfig:
		var m protocol.Config
		if json.Unmarshal(raw, &m) == nil {
			d.emitConfig(m)
		}
	case protocol.TypeSendspinTokenRequest:
		d.emitSendspinTokenRequest()
	case protocol.TypePing:
		// A ping with no id is a keepalive, and is answered with a pong
		// that has no id either. One with an id is answered with that id
		// echoed back, which is how the controller pairs the two and
		// measures the round trip.
		var m protocol.Ping
		if json.Unmarshal(raw, &m) == nil {
			_ = p.writeJSON(protocol.Pong{
				Type: protocol.TypePong,
				ID:   m.ID,
				Mono: monoMillis(),
			})
		}
	case protocol.TypeBeamLock, protocol.TypeBeamUnlock:
		// The beamformer lock is the Echo's own hardware concern; a device
		// with one perimeter microphone has nothing to lock.
	}

	d.emitRaw(msg)
}

// handleAck records the controller's capabilities.
func (d *Device) handleAck(raw []byte) {
	var m protocol.Ack
	if err := json.Unmarshal(raw, &m); err != nil {
		return
	}
	d.mu.Lock()
	d.features = map[string]bool{}
	for _, f := range m.Features {
		d.features[f] = true
	}
	d.mu.Unlock()
	d.state.SetFeatures(m.Features)
	d.log.Info("registered", "features", m.Features)
}

// closeAll closes every open plane.
func (d *Device) closeAll() {
	d.mu.Lock()
	control, data := d.control, d.data
	d.control, d.data = nil, nil
	if d.listenAckTimer != nil {
		d.listenAckTimer.Stop()
		d.listenAckTimer = nil
	}
	if d.listenMaxTimer != nil {
		d.listenMaxTimer.Stop()
		d.listenMaxTimer = nil
	}
	d.listenSession, d.listenSeq = 0, 0
	d.mu.Unlock()

	if control != nil {
		control.Close()
	}
	if data != nil {
		data.Close()
	}
	d.closeShell()
	d.state.SetLink(false)
}

func (d *Device) ackListenSession(session uint32) {
	d.mu.Lock()
	if d.listenSession == session && d.listenAckTimer != nil {
		d.listenAckTimer.Stop()
		d.listenAckTimer = nil
	}
	d.mu.Unlock()
}

// monoMillis is the device's monotonic clock in milliseconds, any fixed
// origin. The controller maps it onto its own to date the captured audio,
// so the origin does not matter but the rate does.
func monoMillis() int64 {
	return time.Since(deviceEpoch).Milliseconds()
}

// deviceEpoch is the fixed origin of the device's monotonic clock.
var deviceEpoch = time.Now()
