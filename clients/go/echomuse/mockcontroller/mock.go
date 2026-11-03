// Package mockcontroller is a fake EchoMuse controller.
//
// It speaks enough of the controller's side of the contract to bring a
// device up, register it, approve it, push a config, drive a voice turn and
// hand out a link credential. It exists so that an application built on the
// SDK can be developed and tested without the real controller, and so that
// the SDK's own tests have something to talk to.
//
// It is a test double, not an emulator. It implements the parts of the
// protocol a device meets in a normal session and nothing more: no wake
// word scoring, no Home Assistant, no Bluetooth, no firmware updates.
//
// # What it does
//
//   - accepts the three WebSocket planes: /control, /data and /shell/{id}
//   - answers a registration with an ack carrying the controller's
//     features, or with a pending or a refused when told to
//   - issues a link credential over the shell plane, exactly as the real
//     controller does, so the device's credential path is exercised
//   - pushes a config and the ring frames
//   - can drive a turn: request the microphone, send playback, end it
package mockcontroller

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/protocol"
)

// Mode is what the mock answers a registration with.
type Mode int

const (
	// ModeApprove admits the device.
	ModeApprove Mode = iota
	// ModePending holds the device for an administrator.
	ModePending
	// ModeRefused refuses the device's credentials.
	ModeRefused
)

// Options configures a mock controller.
type Options struct {
	// Mode is what the registration is answered with.
	Mode Mode
	// Features are the controller's features, sent on the ack.
	Features []string
	// Log receives the mock's own diagnostics.
	Log *slog.Logger
}

// Controller is a running mock.
type Controller struct {
	opts Options
	log  *slog.Logger

	server   *httptest.Server
	upgrader websocket.Upgrader

	mu       sync.Mutex
	control  *websocket.Conn
	data     *websocket.Conn
	shell    *websocket.Conn
	deviceID string
	features []string

	// identified is the device id the data plane identified itself with.
	identified string

	// pongIDs are the ids of the pongs the device has answered, as raw
	// JSON, so a test can see that the id came back byte for byte.
	pongIDs []string

	// shellGen counts the shell sessions the device has opened, so a
	// caller can wait for a fresh one rather than the one before it.
	shellGen int

	// writeMu serialises writes to the control plane.
	//
	// A gorilla WebSocket allows one concurrent writer, and the mock
	// writes from two goroutines: the read loop that answers a
	// registration, and whatever the test is doing. Without this the
	// second writer corrupts the first one's frame.
	writeMu sync.Mutex

	registered   chan struct{}
	registerOnce sync.Once

	micFrames      int
	speakerFrames  int
	credentialSeen bool
	filesReceived  map[string][]byte
	approvals      map[string]bool
}

// New starts a mock controller.
//
// The returned controller listens on a local address. Close it when the
// test is over.
func New(opts Options) *Controller {
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	if opts.Features == nil {
		opts.Features = []string{
			protocol.FeatureBLEAdvertsData,
			protocol.FeatureListenSession,
			protocol.FeatureOutputChain,
			protocol.FeatureBLEConnect,
		}
	}

	c := &Controller{
		opts:          opts,
		log:           opts.Log,
		registered:    make(chan struct{}),
		filesReceived: map[string][]byte{},
		approvals:     map[string]bool{},
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/control", c.handleControl)
	mux.HandleFunc("/data", c.handleData)
	mux.HandleFunc("/shell/", c.handleShell)
	mux.HandleFunc("/events", c.handleEvents)

	c.server = httptest.NewServer(mux)
	return c
}

// URL returns the mock's base URL.
func (c *Controller) URL() string {
	return "ws" + strings.TrimPrefix(c.server.URL, "http")
}

// Host returns the mock's host and port.
func (c *Controller) Host() string {
	return strings.TrimPrefix(c.server.URL, "http://")
}

// Close stops the mock.
func (c *Controller) Close() {
	c.mu.Lock()
	for _, conn := range []*websocket.Conn{c.control, c.data, c.shell} {
		if conn != nil {
			conn.Close()
		}
	}
	c.mu.Unlock()
	c.server.Close()
}

// Registered returns a channel closed once a device has registered.
func (c *Controller) Registered() <-chan struct{} { return c.registered }

// DeviceID returns the id the device registered as.
func (c *Controller) DeviceID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.deviceID
}

// Identified returns the id the data plane identified itself with.
//
// It is empty until the data plane has identified, and it is the id the
// controller would route audio by.
func (c *Controller) Identified() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.identified
}

// PongIDs returns the ids of the pongs the device has answered, as raw
// JSON.
//
// The controller pairs a pong with the ping it sent by the id the pong
// carries, so the id's type matters as much as its value: a ping sent
// with an integer id and answered with the same number written as a
// string is not an answer the controller can pair.
func (c *Controller) PongIDs() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.pongIDs...)
}

// MicFrames returns how many capture frames the device sent.
func (c *Controller) MicFrames() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.micFrames
}

// SpeakerFrames returns how many playback frames the device received.
func (c *Controller) SpeakerFrames() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.speakerFrames
}

// CredentialSeen reports whether the device stored the issued token.
func (c *Controller) CredentialSeen() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.credentialSeen
}

// FileReceived returns the bytes of a file the device was pushed.
func (c *Controller) FileReceived(path string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	data, ok := c.filesReceived[path]
	return data, ok
}

// handleControl accepts the control plane.
func (c *Controller) handleControl(w http.ResponseWriter, r *http.Request) {
	conn, err := c.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	c.mu.Lock()
	c.control = conn
	c.mu.Unlock()

	// The keepalive. The real controller pings every five seconds over
	// JSON, with a sequence id it pairs the reply against, and every
	// twenty seconds over the WebSocket protocol. A mock that pings
	// never would never catch a device that cannot answer.
	stopPing := make(chan struct{})
	defer close(stopPing)
	go c.ping(conn, stopPing)

	for {
		msgType, data, err := conn.ReadMessage()
		if err != nil {
			c.mu.Lock()
			if c.control == conn {
				c.control = nil
			}
			c.mu.Unlock()
			return
		}
		if msgType != websocket.TextMessage {
			continue
		}

		var head struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(data, &head) != nil {
			continue
		}

		switch head.Type {
		case protocol.TypeRegister:
			var reg protocol.Register
			json.Unmarshal(data, &reg)
			c.mu.Lock()
			c.deviceID = reg.DeviceID
			c.mu.Unlock()
			c.registerOnce.Do(func() { close(c.registered) })
			c.answerRegister()
			// The controller closes the connection to a device it is
			// holding, and the device redials. A mock that kept the
			// connection open would let a device sit in a state the
			// real controller would never leave it in.
			if c.holdingPending() {
				conn.Close()
				return
			}
		case protocol.TypeLog:
			var l protocol.Log
			json.Unmarshal(data, &l)
			c.log.Info("device log", "level", l.Level, "message", l.Message)
		case protocol.TypeButton:
			var b protocol.ButtonEvent
			json.Unmarshal(data, &b)
			c.log.Info("button", "down", b.Down, "heldMs", b.HeldMs)
		case protocol.TypeOWWWake:
			c.log.Info("the device reported a wake")
		case protocol.TypeListenState:
			var l protocol.ListenState
			json.Unmarshal(data, &l)
			c.log.Info("listen state", "state", l.State, "reason", l.Reason)
		case protocol.TypePong:
			var pong struct {
				ID json.RawMessage `json:"id"`
			}
			if json.Unmarshal(data, &pong) == nil && len(pong.ID) > 0 {
				c.mu.Lock()
				c.pongIDs = append(c.pongIDs, string(pong.ID))
				c.mu.Unlock()
			}
		}
	}
}

// ping sends the controller's keepalive.
//
// The JSON ping carries a sequence id, which is an integer, and the
// device echoes it back so the controller can pair the two. The
// WebSocket ping is the one that detects a half-open socket.
func (c *Controller) ping(conn *websocket.Conn, stop <-chan struct{}) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	var seq int
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			seq++
			c.writeMu.Lock()
			err := conn.WriteJSON(map[string]any{
				"type": protocol.TypePing,
				"id":   seq,
			})
			c.writeMu.Unlock()
			if err != nil {
				return
			}
		}
	}
}

// writeControl sends a message on the control plane.
//
// The write is serialised: a gorilla WebSocket allows one concurrent
// writer, and the mock writes from the registration handler and from
// whatever the test is doing at the same time.
func (c *Controller) writeControl(v any) error {
	c.mu.Lock()
	conn := c.control
	c.mu.Unlock()
	if conn == nil {
		return fmt.Errorf("mock: no device is connected")
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return conn.WriteJSON(v)
}

// writeData sends a message on the data plane.
func (c *Controller) writeData(b []byte) error {
	c.mu.Lock()
	conn := c.data
	c.mu.Unlock()
	if conn == nil {
		return fmt.Errorf("mock: the data plane is not connected")
	}
	return conn.WriteMessage(websocket.BinaryMessage, b)
}

// holdingPending reports whether the mock is holding the device rather
// than admitting it.
func (c *Controller) holdingPending() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.approvals[c.deviceID] {
		return false
	}
	return c.opts.Mode == ModePending
}

// answerRegister replies to a registration.
func (c *Controller) answerRegister() {
	c.mu.Lock()
	deviceID := c.deviceID
	approved := c.approvals[deviceID]
	c.mu.Unlock()

	// A device that has been approved is admitted, whatever mode the
	// mock is in. The approval is the one thing that admits a device,
	// and it is recorded rather than sent because the controller closes
	// the connection to a device it is holding.
	if approved {
		c.writeControl(protocol.Ack{
			Type:     protocol.TypeAck,
			Features: c.opts.Features,
		})
		return
	}

	switch c.opts.Mode {
	case ModePending:
		c.writeControl(protocol.Pending{Type: protocol.TypePending})
	case ModeRefused:
		c.writeControl(protocol.Refused{Type: protocol.TypeRefused})
	default:
		c.writeControl(protocol.Ack{
			Type:     protocol.TypeAck,
			Features: c.opts.Features,
		})
	}
}

// handleData accepts the data plane.
func (c *Controller) handleData(w http.ResponseWriter, r *http.Request) {
	conn, err := c.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	// The first message on this plane is an identify, not audio. The real
	// controller reads it to learn which device the connection is for and
	// closes a connection whose first frame is anything else — including a
	// microphone frame, which it has no device id to route. A mock that
	// accepted anything would never catch a device that gets this wrong.
	msgType, data, err := conn.ReadMessage()
	if err != nil {
		return
	}
	if msgType != websocket.TextMessage {
		return
	}
	var identify struct {
		Type     string `json:"type"`
		DeviceID string `json:"device_id"`
	}
	if err := json.Unmarshal(data, &identify); err != nil {
		return
	}
	if identify.Type != protocol.TypeIdentify || identify.DeviceID == "" {
		return
	}

	c.mu.Lock()
	c.data = conn
	c.identified = identify.DeviceID
	c.mu.Unlock()

	for {
		msgType, data, err := conn.ReadMessage()
		if err != nil {
			c.mu.Lock()
			if c.data == conn {
				c.data = nil
			}
			c.mu.Unlock()
			return
		}
		if msgType != websocket.BinaryMessage || len(data) == 0 {
			continue
		}
		switch data[0] {
		case protocol.FrameMic, protocol.FrameListen:
			c.mu.Lock()
			c.micFrames++
			c.mu.Unlock()
		}
	}
}

// handleEvents serves a minimal event stream.
//
// The real controller's event feed is a dashboard feature; the mock has one
// so a client that connects to it does not fail.
func (c *Controller) handleEvents(w http.ResponseWriter, r *http.Request) {
	conn, err := c.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	conn.WriteJSON(map[string]any{"type": "snapshot", "devices": []any{}})
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
	}
}

// handleShell serves the shell plane.
//
// It is where the credential is issued, so it has to be a real shell
// session: the controller writes commands and reads their output, and it
// pushes a file as a base64 heredoc. The mock answers the commands it
// knows and records the files it receives.
func (c *Controller) handleShell(w http.ResponseWriter, r *http.Request) {
	conn, err := c.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	c.mu.Lock()
	c.shell = conn
	c.shellGen++
	c.mu.Unlock()

	s := &shellMock{conn: conn, controller: c}
	s.serve()
}

// shellMock answers the commands the controller sends.
type shellMock struct {
	conn       *websocket.Conn
	controller *Controller
}

// serve reads the controller's commands and answers them.
//
// The frame type is not checked, because the real controller does not
// check it either: it sends its commands as text frames, and the Echo
// firmware reads them without looking. A mock that insisted on binary
// frames would never exercise the path the controller actually takes.
func (s *shellMock) serve() {
	var pending strings.Builder

	for {
		_, data, err := s.conn.ReadMessage()
		if err != nil {
			return
		}

		pending.Write(data)
		text := pending.String()

		// A heredoc body arrives in pieces, so the mock only answers
		// once it has the whole command.
		if s.inHeredoc(text) {
			if !strings.Contains(text, "\nEMSDK_B64_EOF\n") &&
				!strings.Contains(text, "\n__END_B64_42__\n") {
				continue
			}
		} else if !strings.HasSuffix(text, "\n") {
			continue
		}
		pending.Reset()

		s.respond(text)
	}
}

// inHeredoc reports whether the text is inside a heredoc body.
func (s *shellMock) inHeredoc(text string) bool {
	for _, delim := range []string{"EMSDK_B64_EOF", "__END_B64_42__"} {
		if idx := strings.Index(text, "<< '"+delim+"'"); idx >= 0 {
			rest := text[idx:]
			if !strings.Contains(rest, "\n"+delim+"\n") {
				return true
			}
		}
	}
	return false
}

// respond answers one command.
func (s *shellMock) respond(text string) {
	// The decoder probe: the mock pretends to be a device with a plain
	// base64 and a plain md5sum, which is what any Linux device has.
	if strings.Contains(text, "DECODER:") {
		s.write("DECODER:base64\nMD5:plain\nDESTDIR:ok\n__DETECT_DONE__\n")
		return
	}

	// The heredoc: the base64 body arrives after the opening line.
	if delim, body, ok := heredoc(text); ok {
		s.receiveFile(body, delim)
		s.write("TRANSFER_OK\n")
		return
	}

	// The verification step.
	if strings.Contains(text, "$GOT") {
		s.write("VERIFY_OK\n")
		return
	}

	s.write("")
}

// heredoc extracts the delimiter and the body of a heredoc.
func heredoc(text string) (string, string, bool) {
	for _, delim := range []string{"EMSDK_B64_EOF", "__END_B64_42__"} {
		open := "<< '" + delim + "'"
		idx := strings.Index(text, open)
		if idx < 0 {
			continue
		}
		rest := text[idx+len(open):]
		lineEnd := strings.Index(rest, "\n")
		if lineEnd < 0 {
			return "", "", false
		}
		body := rest[lineEnd+1:]
		end := strings.Index(body, "\n"+delim+"\n")
		if end < 0 {
			return "", "", false
		}
		return delim, body[:end], true
	}
	return "", "", false
}

// receiveFile decodes a pushed file and records it.
func (s *shellMock) receiveFile(body, delim string) {
	// The destination is named in the command that opened the heredoc.
	dest := s.destination(body)
	clean := strings.NewReplacer("\n", "", "\r", "").Replace(body)
	if clean == "" {
		return
	}
	raw, err := base64.StdEncoding.DecodeString(clean)
	if err != nil {
		s.controller.log.Warn("the device pushed a file that did not decode", "error", err)
		return
	}
	sum := md5.Sum(raw)
	s.controller.log.Info("received a file", "path", dest, "bytes", len(raw),
		"md5", hex.EncodeToString(sum[:]))

	s.controller.mu.Lock()
	s.controller.filesReceived[dest] = raw
	s.controller.mu.Unlock()
}

// destination reads the path a heredoc writes to.
func (s *shellMock) destination(body string) string {
	// The path is in the command line, which the mock recorded when the
	// heredoc began. It is recovered from the controller's own state.
	s.controller.mu.Lock()
	defer s.controller.mu.Unlock()
	for path := range s.controller.filesReceived {
		if strings.Contains(body, path) {
			return path
		}
	}
	return ""
}

// write sends output back to the controller.
func (s *shellMock) write(out string) {
	if out == "" {
		return
	}
	s.conn.WriteMessage(websocket.BinaryMessage, []byte(out))
}

// IssueCredential pushes a link credential to the device over the shell
// plane.
//
// It is what the real controller does when a device is approved, and it is
// the step that makes a device a first-class device: with a credential it
// can be on the encrypted link, and the controller recognises it.
// Approve admits a device that was held pending.
//
// The real controller's approval is a dashboard action: an administrator
// approves the device, and that approval is what issues the link
// credential. The mock does the same thing, so the flow an emulator walks
// through against the mock is the flow it walks through against a real
// controller.
//
// The controller closes the connection to a device it is holding, so the
// device is not connected at the moment of the approval. The approval is
// therefore recorded rather than sent, and the device's next redial is
// the one that is admitted — which is exactly what the real controller
// does.
func (c *Controller) Approve() error {
	c.mu.Lock()
	deviceID := c.deviceID
	c.mu.Unlock()

	if deviceID == "" {
		return fmt.Errorf("mock: no device has registered")
	}

	c.mu.Lock()
	c.approvals[deviceID] = true
	c.mu.Unlock()
	return nil
}

// IssueCredential pushes a link credential to the device over the shell
// plane.
//
// It is what the real controller does when a device is approved, and it is
// the step that makes a device a first-class device: with a credential it
// can be on the encrypted link, and the controller recognises it.
func (c *Controller) IssueCredential(token string, caPEM []byte) error {
	c.mu.Lock()
	control := c.control
	// The generation the next shell session will have. The device opens
	// it in answer to the shell_open below.
	gen := c.shellGen + 1
	c.mu.Unlock()

	if control == nil {
		return fmt.Errorf("mock: no device is connected")
	}
	if err := c.writeControl(protocol.ShellOpen{Type: protocol.TypeShellOpen}); err != nil {
		return err
	}

	// The device dials the shell plane when it is asked to, so the mock
	// waits for that connection before pushing anything.
	//
	// It waits for a *fresh* one. The controller runs one shell session
	// per operation and opens a new one for each, and the session before
	// it is closed by the time the next one is asked for. Waiting for any
	// connection would find the closed one and fail.
	var shell *websocket.Conn
	deadline := time.Now().Add(10 * time.Second)
	for {
		c.mu.Lock()
		shell = c.shell
		shellGen := c.shellGen
		c.mu.Unlock()
		if shell != nil && shellGen == gen {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("mock: the device did not open a shell")
		}
		time.Sleep(20 * time.Millisecond)
	}

	dir := "/data/local/etc/echomuse"
	if err := c.pushFile(shell, dir+"/ca.pem", caPEM, "644"); err != nil {
		return err
	}
	if err := c.pushFile(shell, dir+"/token", []byte(token), "600"); err != nil {
		return err
	}

	c.mu.Lock()
	c.credentialSeen = true
	c.mu.Unlock()

	c.writeControl(protocol.ShellClose{Type: protocol.TypeShellClose})
	return nil
}

// pushFile sends a file over the shell plane as a base64 heredoc.
//
// The mock cannot read the file back — it is the device that holds it —
// so it records the path and the bytes it sent.
func (c *Controller) pushFile(shell *websocket.Conn, dest string, data []byte, mode string) error {
	sum := md5.Sum(data)
	want := hex.EncodeToString(sum[:])
	encoded := base64.StdEncoding.EncodeToString(data)

	c.mu.Lock()
	c.filesReceived[dest] = data
	c.mu.Unlock()

	cmd := fmt.Sprintf(
		"base64 -d > %s.part << 'EMSDK_B64_EOF'\n%s\nEMSDK_B64_EOF\nchmod %s %s.part && echo TRANSFER_OK\n",
		dest, encoded, mode, dest)
	if err := shell.WriteMessage(websocket.TextMessage, []byte(cmd)); err != nil {
		return err
	}

	verify := fmt.Sprintf(
		"GOT=$(md5sum %s.part 2>/dev/null | cut -d' ' -f1); "+
			"case \"$GOT\" in %s*) mv %s.part %s && echo VERIFY_OK ;; "+
			"*) rm -f %s.part; echo \"VERIFY_BAD:$GOT\" ;; esac\n",
		dest, want, dest, dest, dest)
	return shell.WriteMessage(websocket.TextMessage, []byte(verify))
}

// PushConfig sends a configuration push to the device.
func (c *Controller) PushConfig(config protocol.Config) error {
	c.mu.Lock()
	control := c.control
	c.mu.Unlock()
	if control == nil {
		return fmt.Errorf("mock: no device is connected")
	}
	config.Type = protocol.TypeConfig
	return c.writeControl(config)
}

// SendLEDs sends an LED frame to the device.
func (c *Controller) SendLEDs(leds []protocol.LED, listening bool) error {
	c.mu.Lock()
	control := c.control
	c.mu.Unlock()
	if control == nil {
		return fmt.Errorf("mock: no device is connected")
	}
	return c.writeControl(protocol.LEDs{
		Type:      protocol.TypeLEDs,
		LEDs:      leds,
		Listening: &listening,
	})
}

// SendLEDAnim sends an animation spec to the device.
func (c *Controller) SendLEDAnim(anim protocol.AnimSpec) error {
	c.mu.Lock()
	control := c.control
	c.mu.Unlock()
	if control == nil {
		return fmt.Errorf("mock: no device is connected")
	}
	return c.writeControl(protocol.LEDAnim{Type: protocol.TypeLEDAnim, Anim: anim})
}

// RequestMic asks the device to stream microphone audio.
//
// A locked request is a bounded turn; an unlocked one is the always-on
// wake stream.
func (c *Controller) RequestMic(lockMic bool) error {
	c.mu.Lock()
	control := c.control
	c.mu.Unlock()
	if control == nil {
		return fmt.Errorf("mock: no device is connected")
	}
	return c.writeControl(protocol.MicStart{Type: protocol.TypeMicStart, LockMic: lockMic})
}

// StopMic asks the device to stop streaming.
func (c *Controller) StopMic() error {
	c.mu.Lock()
	control := c.control
	c.mu.Unlock()
	if control == nil {
		return fmt.Errorf("mock: no device is connected")
	}
	return c.writeControl(protocol.MicStop{Type: protocol.TypeMicStop})
}

// SendPlayback sends playback audio to the device.
//
// The samples are signed 16-bit little-endian mono at 48 kHz, which is the
// rate the playback plane uses — the capture plane is 16 kHz.
func (c *Controller) SendPlayback(pcm []byte) error {
	c.mu.Lock()
	data := c.data
	c.mu.Unlock()
	if data == nil {
		return fmt.Errorf("mock: the data plane is not connected")
	}
	c.mu.Lock()
	c.speakerFrames++
	c.mu.Unlock()
	return c.writeData(protocol.EncodeSpeaker(pcm))
}

// EndPlayback ends the playback stream.
func (c *Controller) EndPlayback() error {
	c.mu.Lock()
	data := c.data
	c.mu.Unlock()
	if data == nil {
		return fmt.Errorf("mock: the data plane is not connected")
	}
	return c.writeData(protocol.EncodeSpeakerEOS())
}
