package echomuse

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/transport"
)

// The shell plane is how the controller reaches a device's filesystem.
//
// It matters to a device more than it might appear: the controller issues
// the link credential by pushing the token and the certificate authority
// over this plane, and a device that does not serve it never receives one.
// Without a credential the device stays on the plain link, forever
// unconfirmed, and it is not a first-class device.
//
// The protocol is a shell, not a command API. The controller sends shell
// command lines and reads their output, and it pushes a file as a base64
// heredoc. So the SDK's job is to give the controller a real shell and let
// the shell's own grammar handle the heredoc — the SDK does not need to
// understand a byte of the transfer to make it work.

// ShellSession is a running command session.
//
// It is a pipe: whatever is written to it is the session's input, and
// whatever comes out of it is the session's output.
type ShellSession interface {
	io.ReadWriter
	Close() error
}

// ShellRunner starts a shell session.
//
// An application that must not expose a general shell — a device with
// sensitive state, or one whose filesystem is not a shell's to give —
// implements this itself and answers only what it chooses.
type ShellRunner interface {
	// Start begins a session. The pty flag asks for a terminal rather
	// than a pipe, which the controller uses for an interactive shell.
	Start(ctx context.Context, pty bool) (ShellSession, error)
}

// LocalShell starts sessions with the system's shell.
//
// It is the default, and it is right for a device that is a general-purpose
// computer: a Raspberry Pi, a box on a desk, a laptop.
type LocalShell struct {
	// Path is the shell to run. Empty means the system's default.
	Path string
	// Dir is the working directory sessions start in.
	Dir string
	// PathMap rewrites a path the controller named into one on this
	// device.
	//
	// The controller names an absolute path, and the one it uses for the
	// link credential is the Echo's: /data/local/etc/echomuse. A device
	// that stores its state elsewhere would have the credential land
	// somewhere it does not read, and it would stay on the plain link
	// forever. Mapping the path is how the device says where its own
	// state lives.
	PathMap func(path string) string
}

// Start begins a shell session.
func (s LocalShell) Start(ctx context.Context, pty bool) (ShellSession, error) {
	path := s.Path
	if path == "" {
		path = "/bin/sh"
	}

	cmd := exec.CommandContext(ctx, path)
	cmd.Dir = s.Dir

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("echomuse: opening the shell's input: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("echomuse: opening the shell's output: %w", err)
	}
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("echomuse: starting the shell: %w", err)
	}

	return &localSession{
		cmd:    cmd,
		stdin:  stdin,
		stdout: stdout,
	}, nil
}

// localSession is a session backed by a process.
type localSession struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdout    io.ReadCloser
	closeOnce sync.Once
}

// Read reads the session's output.
func (s *localSession) Read(p []byte) (int, error) { return s.stdout.Read(p) }

// Write feeds the session's input.
func (s *localSession) Write(p []byte) (int, error) { return s.stdin.Write(p) }

// Close ends the session and waits for the process.
func (s *localSession) Close() error {
	var err error
	s.closeOnce.Do(func() {
		s.stdin.Close()
		err = s.cmd.Wait()
	})
	return err
}

// shellSession is one live shell connection to the controller.
type shellSession struct {
	conn    *websocket.Conn
	session ShellSession
	rewrite func(string) string

	mu     sync.Mutex
	closed bool
}

// openShell dials the shell plane when the controller asks for it.
//
// The device dials out, because the controller cannot reach into a device
// that is behind a NAT or on a different subnet. The connection comes from
// the same address as the control plane, which is what the controller
// checks: a data or shell connection from anywhere else is refused.
func (d *Device) openShell(pty bool) {
	link, creds := d.link, d.creds
	d.mu.Lock()
	old := d.shell
	d.shell = nil
	d.mu.Unlock()

	// A second shell_open replaces the session, rather than being ignored.
	//
	// The controller runs one shell session per operation — a command, then a
	// file transfer — and it opens a new one for each. A device that refuses
	// the second request leaves the controller waiting on a session that
	// never arrives, and it reports that as the device having no shell at
	// all.
	if old != nil {
		old.close()
	}

	if link.Host == "" {
		d.log.Warn("the controller asked for a shell before the link was up")
		return
	}

	runner := d.opts.ShellRunner
	if runner == nil {
		// The default shell maps the controller's credential path onto the
		// directory this device reads its credentials from, so a device
		// that does not use the Echo's paths still gets them.
		local := LocalShell{}
		if d.opts.CredentialsDir != "" {
			local.PathMap = func(p string) string {
				if p == ControllerCredentialPath {
					return d.opts.CredentialsDir
				}
				return p
			}
		}
		runner = local
	}

	go func() {
		// The credential is re-read at dial time, not taken from the
		// connection's copy. The credential arrives ON this plane: the
		// controller pushes the certificate authority and the token
		// through a shell session, and the session that carries them in
		// is the one that has to dial with the new credential afterwards.
		//
		// A device that dials with the credential it had when the link
		// opened never gets to the encrypted link — the one credential it
		// needs in order to present the credential it was just given.
		creds = d.freshCredentials(creds)

		// The device is paired once it holds the controller's certificate
		// authority, so the pairing request stops. A request that keeps
		// repeating after the approval leaves the dashboard asking for an
		// approval the device already has.
		if len(creds.CAPEM) > 0 {
			d.mu.Lock()
			d.creds = creds
			d.mu.Unlock()
			d.StopPairing()
		}

		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		conn, err := d.dialShell(ctx, link, creds, pty)
		if err != nil {
			d.log.Warn("the shell connection failed", "error", err)
			return
		}

		session, err := runner.Start(context.Background(), pty)
		if err != nil {
			d.log.Warn("cannot start a shell session", "error", err)
			conn.Close()
			return
		}

		sh := &shellSession{conn: conn, session: session}
		if local, ok := runner.(LocalShell); ok && local.PathMap != nil {
			sh.rewrite = local.PathMap
		}
		d.mu.Lock()
		d.shell = sh
		d.mu.Unlock()

		d.log.Info("shell session open")
		d.serveShell(sh)

		d.mu.Lock()
		if d.shell == sh {
			d.shell = nil
		}
		d.mu.Unlock()
	}()
}

// closeShell ends the shell session.
func (d *Device) closeShell() {
	d.mu.Lock()
	s := d.shell
	d.shell = nil
	d.mu.Unlock()
	if s != nil {
		s.close()
	}
}

// dialShell opens the shell plane.
//
// The shell plane carries the link credential to the device, so it has to
// speak the same security as the other two: a device holding the
// controller's certificate authority dials wss and presents its token, and
// a device without one dials plain and presents nothing.
//
// It also falls back to plain while a pairing window is open, for the same
// reason the control plane does: a device being paired with a new
// controller cannot open the encrypted link until it has been issued that
// controller's certificate authority, and the shell session is what
// carries it. The fallback sends no token — the credential belongs to the
// controller that issued it.
func (d *Device) dialShell(ctx context.Context, link transport.Controller, creds transport.Credentials, pty bool) (*websocket.Conn, error) {
	bases := d.dialBases(link, creds)
	if len(bases) == 0 {
		return nil, fmt.Errorf("echomuse: the device holds a certificate authority but the controller offers no encrypted port")
	}

	var lastErr error
	for _, base := range bases {
		target := base + "/shell/" + d.opts.DeviceID
		if pty {
			target += "?pty=1"
		}

		// The dialer is built here rather than reused from the control
		// plane, because the credential can have changed since that plane
		// was opened — the certificate authority arrives on this very
		// connection, and the token with it.
		dialer := &websocket.Dialer{
			HandshakeTimeout:  15 * time.Second,
			EnableCompression: false,
			TLSClientConfig:   tlsConfigFor(creds),
		}

		header := http.Header{}
		if creds.Token != "" && !strings.HasPrefix(base, "ws://") {
			header.Set(TokenHeader, creds.Token)
		}

		conn, resp, err := dialer.DialContext(ctx, target, header)
		if resp != nil {
			resp.Body.Close()
		}
		if err != nil {
			lastErr = fmt.Errorf("echomuse: dialling %s: %w", target, err)
			continue
		}

		// The shell plane carries the keepalive too. The controller pings
		// every plane it serves, and a shell session can sit idle for
		// minutes while a human decides what to type.
		conn.SetReadDeadline(time.Now().Add(pongWait))
		conn.SetPongHandler(func(string) error {
			return conn.SetReadDeadline(time.Now().Add(pongWait))
		})
		go keepalive(conn)

		return conn, nil
	}
	return nil, lastErr
}

// serveShell bridges the WebSocket to the shell session.
//
// The controller writes command lines and reads their output; the shell
// reads the commands and writes the output. Both directions run on their
// own goroutine so neither waits on the other for long, and a command that
// produces output while the controller is still writing arrives as it is
// produced rather than at the end.
func (d *Device) serveShell(s *shellSession) {
	defer s.close()

	var wg sync.WaitGroup
	wg.Add(2)

	// The controller's commands into the shell.
	//
	// The frame type is deliberately not checked. The controller sends shell
	// commands as text frames and the Echo firmware ignores the type
	// entirely; a device that insists on binary frames reads nothing, the
	// shell produces no output, and the controller gives up waiting for it.
	go func() {
		defer wg.Done()
		for {
			_, data, err := s.conn.ReadMessage()
			if err != nil {
				return
			}
			s.conn.SetReadDeadline(time.Now().Add(pongWait))
			if s.rewrite != nil {
				data = s.rewritePaths(data)
			}
			if _, err := s.session.Write(data); err != nil {
				return
			}
		}
	}()

	// The shell's output back to the controller.
	go func() {
		defer wg.Done()
		buf := make([]byte, 4096)
		for {
			n, err := s.session.Read(buf)
			if n > 0 {
				if werr := s.conn.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	wg.Wait()
}

// close shuts the session down.
func (s *shellSession) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	s.conn.Close()
	s.session.Close()
}

// freshCredentials re-reads the link credential from disk.
//
// The credential arrives over the shell plane, so the shell plane has to
// pick it up from there: a session that dials with the credential the link
// was opened with cannot present the credential it was just handed, and
// stays on the plain link for ever.
//
// A read failure is not a failure. The credential the connection already
// holds is used instead, which is the right answer for a device that keeps
// its credential in memory rather than on disk.
func (d *Device) freshCredentials(fallback transport.Credentials) transport.Credentials {
	dir := d.opts.CredentialsDir
	if dir == "" {
		return fallback
	}
	creds, err := transport.LoadCredentials(dir)
	if err != nil {
		return fallback
	}
	if creds.Token == "" && len(creds.CAPEM) == 0 {
		return fallback
	}
	return creds
}

// rewritePaths maps the controller's paths onto this device's.
//
// The controller names absolute paths, and the credential path it uses is
// the Echo's own: /data/local/etc/echomuse. A device whose state lives
// elsewhere needs the credential to land where it reads it from, so every
// occurrence of the mapped path is replaced.
//
// Only paths the mapping names are touched. A path the mapping does not
// know is left alone, so a command that names an unrelated file still
// refers to that file.
func (s *shellSession) rewritePaths(data []byte) []byte {
	if s.rewrite == nil {
		return data
	}
	text := string(data)
	for _, from := range mappedPaths {
		if from == "" {
			continue
		}
		text = strings.ReplaceAll(text, from, s.rewrite(from))
	}
	return []byte(text)
}

// mappedPaths are the controller-side paths a device may need to remap.
//
// The credential path is the one that matters: it is where the controller
// puts the token and the certificate authority, and where the device has to
// read them from.
var mappedPaths = []string{
	ControllerCredentialPath,
}

// The credential transfer is a base64 heredoc, and the controller checks the
// bytes it sent against the md5 the device reports. These are the pieces
// of that the SDK implements so a device can be issued a credential.

// TransferFile writes bytes to a path on the device and verifies them.
//
// It mirrors what the controller does when it pushes a file, so a device
// that implements this accepts the credential push and every other push the
// controller makes.
//
// The bytes land in a temporary file and are renamed only once their md5
// matches, so a failed write leaves whatever was at the destination
// untouched.
func TransferFile(runner ShellRunner, data []byte, dest, mode string) error {
	if mode == "" {
		mode = "600"
	}
	if !validFileMode(mode) {
		return fmt.Errorf("echomuse: invalid file mode %q", mode)
	}
	if dest == "" {
		return fmt.Errorf("echomuse: destination path is empty")
	}
	dir := filepath.Dir(dest)
	landing := dest + ".part"
	sum := md5.Sum(data)
	wantHex := hex.EncodeToString(sum[:])

	encoded := base64.StdEncoding.EncodeToString(data)
	var body strings.Builder
	body.WriteString("mkdir -p " + shellQuote(dir) + " && ")
	body.WriteString("base64 -d > " + shellQuote(landing) + " << 'EMSDK_B64_EOF'\n")
	body.WriteString(encoded)
	body.WriteString("\nEMSDK_B64_EOF\n")
	body.WriteString("chmod " + mode + " " + shellQuote(landing) + " && echo EMSDK_TRANSFER_OK\n")

	out, err := runCommand(runner, body.String())
	if err != nil {
		return fmt.Errorf("echomuse: writing %s: %w", dest, err)
	}
	if !strings.Contains(out, "EMSDK_TRANSFER_OK") {
		return fmt.Errorf("echomuse: writing %s: the write did not confirm: %s", dest, out)
	}

	verify := fmt.Sprintf(
		"GOT=$(md5sum %s 2>/dev/null | cut -d' ' -f1); "+
			"case \"$GOT\" in %s*) mv %s %s && echo EMSDK_VERIFY_OK ;; "+
			"*) rm -f %s; echo \"EMSDK_VERIFY_BAD:$GOT\" ;; esac",
		shellQuote(landing), wantHex, shellQuote(landing), shellQuote(dest), shellQuote(landing))

	out, err = runCommand(runner, verify)
	if err != nil {
		return fmt.Errorf("echomuse: verifying %s: %w", dest, err)
	}
	if !strings.Contains(out, "EMSDK_VERIFY_OK") {
		return fmt.Errorf("echomuse: %s did not verify: %s", dest, out)
	}
	return nil
}

// validFileMode accepts only an octal permission literal. TransferFile puts
// this value into a shell command, so accepting arbitrary text would turn a
// mode option into command execution.
func validFileMode(mode string) bool {
	if len(mode) != 3 && len(mode) != 4 {
		return false
	}
	for _, r := range mode {
		if r < '0' || r > '7' {
			return false
		}
	}
	return true
}

// runCommand runs one command in a fresh session and returns its output.
//
// The session is closed afterwards, so each command is independent — which
// is how the controller's own shell sessions behave.
func runCommand(runner ShellRunner, cmd string) (string, error) {
	session, err := runner.Start(context.Background(), false)
	if err != nil {
		return "", err
	}
	defer session.Close()

	if _, err := session.Write([]byte(cmd + "\n")); err != nil {
		return "", err
	}

	out, err := io.ReadAll(session)
	if err != nil && err != io.EOF {
		return string(out), err
	}
	return string(out), nil
}

// shellQuote makes a string safe to put in a shell command.
//
// It is the same rule the controller uses: wrap in single quotes and
// escape any single quote inside. Nothing a user supplies is ever
// interpolated into a command without passing through here.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// CredentialsDir is where the SDK looks for the link credentials.
const CredentialsDir = "/data/local/etc/echomuse"

// ControllerCredentialPath is where the controller puts the link
// credential.
//
// It is the Echo's path, and the controller does not ask the device where
// its own state lives — so a device that keeps its credentials elsewhere
// maps this path onto its own.
const ControllerCredentialPath = "/data/local/etc/echomuse"

// WriteCredentials stores the link credentials where the SDK reads them.
//
// The controller pushes them itself over the shell plane; this is the
// application-side equivalent, for a device that is given its credentials
// by other means.
func WriteCredentials(dir, token string, caPEM []byte) error {
	if dir == "" {
		dir = CredentialsDir
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("echomuse: creating %s: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("echomuse: securing %s: %w", dir, err)
	}
	if token != "" {
		if err := writeCredentialFile(filepath.Join(dir, "token"), []byte(token), 0o600); err != nil {
			return fmt.Errorf("echomuse: writing the token: %w", err)
		}
	}
	if len(caPEM) > 0 {
		if err := writeCredentialFile(filepath.Join(dir, "ca.pem"), caPEM, 0o644); err != nil {
			return fmt.Errorf("echomuse: writing the certificate authority: %w", err)
		}
	}
	return nil
}

// writeCredentialFile also sets the mode on an existing file. os.WriteFile's
// mode argument applies only when it creates the file, which could otherwise
// leave a previously world-readable token exposed after a credential update.
func writeCredentialFile(path string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if err := f.Chmod(mode); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
