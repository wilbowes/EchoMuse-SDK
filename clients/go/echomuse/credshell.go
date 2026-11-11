package echomuse

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The credential shell is the default runner for the shell plane.
//
// The controller's shell plane is a shell: it writes command lines and
// reads the output. The SDK needs that plane for exactly one thing — the
// controller issues the device's link credential over it, and a device that
// never receives one stays on the plain link for ever.
//
// A real shell is a much larger thing than that. A controller that can open
// a shell on the device runs every command it writes as the user running
// the device, which under a Home Assistant add-on is every administrator of
// the installation. So the default answers the credential transfer and
// refuses everything else, and a device that wants the whole shell asks for
// it explicitly.

// decoderProbeReply is what the runner answers to the controller's probe.
//
// The controller asks a device what it can do before it pushes a file: it
// wants a base64 decoder, an md5sum and a directory it can write to. A
// runner that does not answer says nothing about itself, and the
// controller has to guess.
const decoderProbeReply = "DECODER:base64\nMD5:plain\nDESTDIR:ok\n__DETECT_DONE__\n"

// CredentialShellRunner returns the shell plane that carries the
// controller's credential transfer and nothing else.
//
// It is exported so an application that serves more than one plane can
// compose them: the credential transfer, and the wake assets the
// controller pushes, are two separate things a device needs.
func CredentialShellRunner(dir string) ShellRunner {
	if dir == "" {
		dir = ControllerCredentialPath
	}
	return credentialShellRunner{dir: dir}
}

// credentialShellRunner serves the shell plane with the credential
// transfer alone.
type credentialShellRunner struct {
	// dir is where the credential files are written.
	dir string
}

// Start begins a session.
//
// The terminal flag is ignored: the credential transfer is not interactive,
// and a session that answers no general command has no terminal to give.
func (r credentialShellRunner) Start(_ context.Context, _ bool) (ShellSession, error) {
	s := &credentialShellSession{
		dir:    r.dir,
		work:   make(chan []byte, 16),
		quit:   make(chan struct{}),
		finish: make(chan struct{}),
	}
	s.ready = sync.NewCond(&s.mu)
	go s.run()
	return s, nil
}

// credentialShellSession is one session on the shell plane.
//
// It is a line reader rather than a shell: it holds the controller's
// commands until each has arrived in full, answers the ones it knows, and
// refuses the rest.
//
// The commands are carried out on their own goroutine, the way a real
// shell carries them out on its own process. That matters for the order
// in which things arrive: the controller pushes its last file and then
// closes the session, and a session that had not finished the file when
// the close arrived would lose it.
type credentialShellSession struct {
	dir string

	work   chan []byte
	quit   chan struct{}
	finish chan struct{}
	once   sync.Once

	mu     sync.Mutex
	ready  *sync.Cond
	buffer strings.Builder
	output bytes.Buffer
	closed bool
}

// Read returns the answer to the last command.
func (s *credentialShellSession) Read(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for s.output.Len() == 0 {
		if s.closed {
			return 0, io.EOF
		}
		s.ready.Wait()
	}
	return s.output.Read(p)
}

// Write takes the controller's command.
//
// Nothing is run here: the command is handed to the goroutine that carries
// the commands out, so the controller's next frame is not held up by this
// one being written to disk.
func (s *credentialShellSession) Write(p []byte) (int, error) {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return 0, io.ErrClosedPipe
	}

	chunk := make([]byte, len(p))
	copy(chunk, p)

	select {
	case s.work <- chunk:
		return len(p), nil
	case <-s.finish:
		return 0, io.ErrClosedPipe
	}
}

// Close ends the session.
//
// The commands that have arrived are carried out first. The controller
// closes the session as soon as it has sent its last command, and a
// command that was dropped there would be a credential that never lands.
func (s *credentialShellSession) Close() error {
	s.once.Do(func() {
		close(s.quit)
		// The commands that have arrived are carried out first, but not
		// for ever. The controller waits fifteen seconds for the device
		// to dial the next shell session, and it dials it only after
		// this one has closed — so a close that waits out a command
		// costs the controller its whole timeout.
		select {
		case <-s.finish:
		case <-time.After(closeDrainTimeout):
		}

		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		s.ready.Broadcast()
	})
	return nil
}

// closeDrainTimeout bounds how long a close waits for the command in
// flight.
//
// The firmware kills the shell process and does not wait for it. This is
// the same idea: the command that is running is a command whose answer
// nobody is going to read.
const closeDrainTimeout = 2 * time.Second

// run carries the commands out.
//
// It stops when the session is closed, after the commands that have
// arrived have been dealt with.
func (s *credentialShellSession) run() {
	defer close(s.finish)

	for {
		select {
		case chunk := <-s.work:
			s.process(chunk)
		case <-s.quit:
			for {
				select {
				case chunk := <-s.work:
					s.process(chunk)
				default:
					return
				}
			}
		}
	}
}

// process carries out the commands in a chunk.
//
// The buffer belongs to the goroutine that carries the commands out, so
// it is not under the lock the reader uses.
func (s *credentialShellSession) process(chunk []byte) {
	s.buffer.Write(chunk)
	for {
		text := s.buffer.String()
		trimmed := strings.TrimLeft(text, " \t\r\n")
		if trimmed == "" {
			s.buffer.Reset()
			return
		}

		consumed, out, complete := s.execute(trimmed)
		if !complete {
			return
		}
		rest := trimmed[consumed:]
		s.buffer.Reset()
		s.buffer.WriteString(rest)
		if out != "" {
			s.mu.Lock()
			s.output.WriteString(FrameAnswer(out))
			s.mu.Unlock()
			s.ready.Broadcast()
		}
		if rest == "" {
			return
		}
	}
}

// execute answers one command.
//
// It reports incomplete when the command has not all arrived, so a command
// split across two frames is not answered half way.
func (s *credentialShellSession) execute(text string) (int, string, bool) {
	// The decoder probe.
	if idx := strings.Index(text, "DECODER:"); idx >= 0 {
		end := strings.Index(text[idx:], "\n")
		if end < 0 {
			return 0, "", false
		}
		return idx + end + 1, decoderProbeReply, true
	}

	// The directory the credential goes in. The controller makes it
	// before it pushes anything, and a directory that is not there is a
	// transfer that cannot land.
	if cmd, state := parseMkdir(text); state == parseMatched {
		return cmd.length, s.makeDirectory(cmd), true
	} else if state == parseIncomplete {
		return 0, "", false
	}

	// The file push, with the mode and the confirmation that follow it.
	if cmd, state := s.parseTransfer(text); state == parseMatched {
		out, err := s.writeCredential(cmd)
		if err != nil {
			return cmd.length, refusalText(err), true
		}
		return cmd.length, out, true
	} else if state == parseIncomplete {
		return 0, "", false
	}

	// The mode on its own, for a controller that sends it separately.
	if cmd, state := parseChmod(text); state == parseMatched {
		return cmd.length, s.applyChmod(cmd), true
	} else if state == parseIncomplete {
		return 0, "", false
	}

	// The check on the file it pushed.
	if cmd, state := parseVerify(text); state == parseMatched {
		return cmd.length, s.verifyCredential(cmd), true
	} else if state == parseIncomplete {
		return 0, "", false
	}

	// Not a command this runner answers. The line is waited out first, so
	// a command that arrives in two pieces is not refused half way.
	if !strings.HasSuffix(text, "\n") {
		return 0, "", false
	}
	return len(text), refusalText(fmt.Errorf("this plane carries the credential transfer only")), true
}

// mkdirCommand is a directory the controller asks for.
type mkdirCommand struct {
	// path is the directory.
	path string
	// length is how much of the input the command used.
	length int
}

// parseMkdir reads a directory the controller asks for.
//
// The controller makes the credential directory before it pushes anything
// into it, and asks the question in the same round trip as the digest
// check — so the answer has to say whether the directory is there.
func parseMkdir(text string) (mkdirCommand, parseState) {
	fields := shellFields(text)
	if len(fields) < 2 || fields[0] != "mkdir" {
		return mkdirCommand{}, parseNoMatch
	}
	// "mkdir -p PATH" — the flag is skipped.
	path := fields[1]
	for i, f := range fields {
		if f == "-p" && i+1 < len(fields) {
			path = fields[i+1]
			break
		}
	}
	lineEnd := strings.Index(text, "\n")
	if lineEnd < 0 {
		return mkdirCommand{}, parseIncomplete
	}
	return mkdirCommand{path: path, length: lineEnd + 1}, parseMatched
}

// makeDirectory makes the directory the credential goes in.
//
// The controller asks the question and reads the answer to see whether
// the directory is there, so the answer names it.
func (s *credentialShellSession) makeDirectory(cmd mkdirCommand) string {
	clean := filepath.Clean(unquotePath(cmd.path))
	// Only the credential directory is made. A plane that could make a
	// directory anywhere on the device could make a directory anywhere
	// on the device.
	if clean != ControllerCredentialPath && clean != s.dir {
		return refusalText(fmt.Errorf("%s is outside the credential directory", cmd.path))
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return refusalText(fmt.Errorf("the credential directory could not be created: %w", err))
	}
	return "DESTDIR:ok\n"
}

// refusalText is what the runner answers to a command it does not carry
// out.
//
// The controller reads the output looking for the markers it expects, so
// an answer that is not one of them is a command that was not run.
func refusalText(err error) string {
	return "echomuse: refused: " + err.Error() + "\n"
}

// parseState is how a parse ended.
type parseState int

const (
	// parseNoMatch means the text is not that command.
	parseNoMatch parseState = iota
	// parseMatched means the text is that command.
	parseMatched
	// parseIncomplete means the text is that command, and more of it is
	// still on its way.
	parseIncomplete
)

// transferCommand is the controller's file push.
type transferCommand struct {
	// dest is the path the controller named.
	dest string
	// payload is the decoded content.
	payload []byte
	// mode is the permission the controller asked for.
	mode string
	// marker is the text the controller asks to have echoed back.
	marker string
	// length is how much of the input the command used.
	length int
}

// parseTransfer reads the controller's base64 heredoc.
//
// The controller pushes a file as a base64 heredoc. It names the decoder
// it wants, the destination, and the mode, and it asks for the
// confirmation:
//
//	base64 -d << 'DELIM' > PATH && chmod MODE PATH && echo TRANSFER_OK
//	<base64>
//	DELIM
//
// The decoder may be `base64`, `busybox base64` or a python one-liner, and
// the redirect may come before or after the heredoc, so the command is
// taken apart by what is in it rather than by its exact order.
func (s *credentialShellSession) parseTransfer(text string) (transferCommand, parseState) {
	idx := strings.Index(text, "base64")
	if idx < 0 {
		return transferCommand{}, parseNoMatch
	}
	// Whatever came before the command has to be the end of another
	// command, or the text is something else entirely.
	if before := strings.TrimSpace(text[:idx]); before != "" && !strings.HasSuffix(before, "&&") {
		return transferCommand{}, parseNoMatch
	}

	rest := text[idx:]
	open := strings.Index(rest, " << ")
	if open < 0 {
		return transferCommand{}, parseIncomplete
	}
	after := rest[open+len(" << "):]
	if !strings.HasPrefix(after, "'") {
		return transferCommand{}, parseNoMatch
	}
	end := strings.Index(after[1:], "'")
	if end < 0 {
		return transferCommand{}, parseIncomplete
	}
	delim := after[1 : 1+end]
	after = after[2+end:]

	// The rest of the command line comes after the delimiter, and the body
	// follows it. The controller puts the redirect and the confirmation
	// there, so the command line is everything up to the first newline.
	lineEnd := strings.Index(after, "\n")
	if lineEnd < 0 {
		return transferCommand{}, parseIncomplete
	}
	command := after[:lineEnd]
	body := after[lineEnd+1:]
	terminator := "\n" + delim + "\n"
	// Only the TAIL of the body is searched. The delimiter ends the
	// body, so it cannot be anywhere else in it — and the controller
	// sends a transfer line by line, so searching all of it on every
	// line is quadratic in the size of the transfer.
	stop := -1
	if len(body) <= fullScanLimit {
		stop = strings.Index(body, terminator)
	} else {
		window := len(terminator)
		if window > len(body) {
			window = len(body)
		}
		tail := body[len(body)-window:]
		if found := strings.Index(tail, terminator); found >= 0 {
			stop = len(body) - window + found
		}
	}
	if stop < 0 {
		return transferCommand{}, parseIncomplete
	}
	payload := body[:stop]
	tail := body[stop+len(terminator):]

	// The destination is named by the redirect, which may come before or
	// after the heredoc.
	dest, ok := parseRedirectDest(text[:idx+open], command)
	if !ok {
		return transferCommand{}, parseNoMatch
	}

	// The destination is checked BEFORE the body is decoded.
	//
	// The body is the largest thing on the wire — a multi-megabyte model
	// is megabytes of base64 — and decoding it allocates all of it. A
	// transfer to a path this plane does not carry is refused, and
	// refusing it must not cost the machine a copy of the body.
	if !s.carriesPath(dest) {
		return transferCommand{}, parseNoMatch
	}

	data, err := base64.StdEncoding.DecodeString(stripWhitespace(payload))
	if err != nil {
		return transferCommand{}, parseNoMatch
	}

	cmd := transferCommand{
		dest:    dest,
		payload: data,
		length:  len(text) - len(tail),
	}

	// The mode and the confirmation are on the command line. A controller
	// that puts them after the body instead is answered from the tail.
	cmd.mode, cmd.marker = parseModeAndMarker(command)
	if tail != "" {
		lineEnd := strings.Index(tail, "\n")
		if lineEnd < 0 {
			return transferCommand{}, parseIncomplete
		}
		tailMode, tailMarker := parseModeAndMarker(tail[:lineEnd])
		if cmd.mode == "" {
			cmd.mode = tailMode
		}
		if cmd.marker == "" {
			cmd.marker = tailMarker
		}
		cmd.length += lineEnd + 1
	}
	return cmd, parseMatched
}

// parseRedirectDest finds the path the controller is writing to.
//
// The redirect is either before the heredoc, as `> PATH << 'DELIM'`, or
// after it, as `<< 'DELIM' > PATH`, and the path is the first word after
// the redirect.
func parseRedirectDest(before, after string) (string, bool) {
	for _, text := range []string{before, after} {
		idx := strings.Index(text, "> ")
		if idx < 0 {
			continue
		}
		path := strings.TrimSpace(text[idx+2:])
		if path == "" {
			continue
		}
		// The path ends at the first space, which is where the mode and
		// the confirmation start.
		if space := strings.IndexAny(path, " \t"); space >= 0 {
			path = path[:space]
		}
		if path != "" {
			return path, true
		}
	}
	return "", false
}

// carriesPath reports whether this plane carries the file at a path.
//
// Only the two credential files are carried. A transfer to anything else
// is not this plane's, and it is refused before its body is decoded —
// the body is the largest thing on the wire, and a plane that decodes
// what it will refuse holds a megabyte of base64 for nothing.
func (s *credentialShellSession) carriesPath(path string) bool {
	clean := filepath.Clean(unquotePath(path))
	if strings.HasSuffix(clean, ".part") {
		clean = strings.TrimSuffix(clean, ".part")
	}
	name := filepath.Base(clean)
	if name != "token" && name != "ca.pem" {
		return false
	}
	for _, candidate := range []string{
		filepath.Join(ControllerCredentialPath, name),
		filepath.Join(s.dir, name),
	} {
		if clean == candidate {
			return true
		}
	}
	return false
}

// chmodCommand is the mode the controller asks for on a file it pushed.
type chmodCommand struct {
	// path is the file the mode is for.
	path string
	// mode is the permission asked for.
	mode string
	// marker is the text the controller asks to have echoed back.
	marker string
	// length is how much of the input the command used.
	length int
}

// parseChmod reads a mode command on its own.
//
// The controller sets the mode on the line after the heredoc, and a
// controller that sends that line in a frame of its own is still asking
// for the mode.
//
// The mode and the path are in the order the shell takes them:
// "chmod MODE PATH". A controller that writes them the other way round
// is read as the other way round, because an octal mode and a path are
// each recognisable on their own.
func parseChmod(text string) (chmodCommand, parseState) {
	fields := shellFields(text)
	if len(fields) < 2 || fields[0] != "chmod" {
		return chmodCommand{}, parseNoMatch
	}
	lineEnd := strings.Index(text, "\n")
	if lineEnd < 0 {
		return chmodCommand{}, parseIncomplete
	}

	cmd := chmodCommand{length: lineEnd + 1}
	// The mode is the octal one and the path is the other. The shell
	// takes "chmod MODE PATH", so that is the order read first.
	if validFileMode(fields[1]) {
		cmd.mode = fields[1]
		if len(fields) > 2 {
			cmd.path = fields[2]
		}
	} else {
		cmd.path = fields[1]
		if len(fields) > 2 {
			cmd.mode = fields[2]
		}
	}
	cmd.marker = markerFrom(text[:lineEnd])
	return cmd, parseMatched
}

// markerFrom reads the marker a command asks to have echoed back.
//
// The controller asks for it with "echo MARKER", and the marker is what
// tells it the command ran.
func markerFrom(line string) string {
	fields := shellFields(line)
	for i, f := range fields {
		if f == "echo" && i+1 < len(fields) {
			return strings.Trim(fields[i+1], `"'`)
		}
	}
	return ""
}

// parseModeAndMarker reads the mode and the confirmation marker out of
// the line that follows a heredoc.
//
// The controller asks for the mode of the file it pushed and for a marker
// to be echoed back, in one line:
//
//	chmod 600 PATH && echo TRANSFER_OK
func parseModeAndMarker(line string) (string, string) {
	fields := shellFields(line)
	mode := ""
	marker := ""
	for i, f := range fields {
		switch {
		case f == "chmod" && i+1 < len(fields) && mode == "":
			mode = fields[i+1]
		case f == "echo" && i+1 < len(fields) && marker == "":
			marker = fields[i+1]
		}
	}
	return mode, marker
}

// verifyCommand is the controller's check on a file it pushed.
type verifyCommand struct {
	// part is the path the file was written to.
	part string
	// dest is the path the file is renamed to.
	dest string
	// pattern is the md5 the controller expects.
	pattern string
	// marker is the text the controller asks to have echoed back.
	marker string
	// length is how much of the input the command used.
	length int
}

// parseVerify reads the controller's md5 check.
//
// The controller compares the md5 of the file it pushed with the md5 it
// sent, and renames the file only when they agree:
//
//	GOT=$(md5sum PATH 2>/dev/null | cut -d' ' -f1);
//	case "$GOT" in <md5>*) mv PATH.part PATH && echo VERIFY_OK ;;
//
// The md5 tool may be named with a prefix — the controller probes for
// "busybox md5sum" before it settles on a plain "md5sum" — so the path
// is looked for after the tool rather than at a fixed place.
func parseVerify(text string) (verifyCommand, parseState) {
	if !strings.HasPrefix(text, "GOT=$(") {
		return verifyCommand{}, parseNoMatch
	}
	if !strings.Contains(text, "md5sum") {
		return verifyCommand{}, parseNoMatch
	}
	if !strings.Contains(text, "esac") {
		return verifyCommand{}, parseIncomplete
	}

	fields := shellFields(text)
	if len(fields) < 3 {
		return verifyCommand{}, parseNoMatch
	}

	cmd := verifyCommand{length: len(text)}
	for i, f := range fields {
		switch {
		case f == "in" && i+1 < len(fields):
			cmd.pattern = strings.TrimSuffix(fields[i+1], "*)")
		case f == "mv" && i+2 < len(fields):
			cmd.dest = fields[i+2]
		case f == "echo" && i+1 < len(fields) && cmd.marker == "":
			cmd.marker = fields[i+1]
		}
	}
	// The file is the one the digest is taken of. It follows the md5
	// tool, which may be named with a prefix — the controller probes for
	// "busybox md5sum" before it settles on a plain "md5sum".
	cmd.part = pathAfterMd5(text)
	if cmd.dest == "" || cmd.part == "" {
		return verifyCommand{}, parseNoMatch
	}
	return cmd, parseMatched
}

// pathAfterMd5 is the path the digest is taken of.
//
// The md5 tool may be named with a prefix, so the path is the word after
// the tool rather than at a fixed place.
func pathAfterMd5(text string) string {
	idx := strings.Index(text, "md5sum")
	if idx < 0 {
		return ""
	}
	rest := strings.TrimLeft(text[idx+len("md5sum"):], " \t")
	end := strings.IndexAny(rest, " \t")
	if end < 0 {
		return ""
	}
	return strings.Trim(rest[:end], `"'`)
}

// writeCredential stores the file the controller pushed.
func (s *credentialShellSession) writeCredential(cmd transferCommand) (string, error) {
	dest, name, err := s.resolve(cmd.dest)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return "", fmt.Errorf("the credential directory could not be created: %w", err)
	}
	if err := writeCredentialFile(dest, cmd.payload, defaultCredentialMode(name)); err != nil {
		return "", fmt.Errorf("the credential could not be written: %w", err)
	}
	if cmd.mode != "" {
		if err := os.Chmod(dest, credentialMode(name, cmd.mode)); err != nil {
			return "", fmt.Errorf("the credential mode could not be set: %w", err)
		}
	}

	marker := cmd.marker
	if marker == "" {
		marker = "TRANSFER_OK"
	}
	return marker + "\n", nil
}

// applyChmod sets the mode the controller asks for on a file it pushed.
func (s *credentialShellSession) applyChmod(cmd chmodCommand) string {
	path, name, err := s.resolve(cmd.path)
	if err != nil {
		return refusalText(err)
	}
	if _, err := os.Stat(path); err != nil {
		return refusalText(fmt.Errorf("the transferred file is missing"))
	}
	if err := os.Chmod(path, credentialMode(name, cmd.mode)); err != nil {
		return refusalText(fmt.Errorf("the credential mode could not be set: %w", err))
	}

	marker := cmd.marker
	if marker == "" {
		return ""
	}
	return marker + "\n"
}

// verifyCredential checks the file the controller pushed and renames it.
func (s *credentialShellSession) verifyCredential(cmd verifyCommand) string {
	part, _, err := s.resolve(cmd.part)
	if err != nil {
		return refusalText(err)
	}
	dest, _, err := s.resolve(cmd.dest)
	if err != nil {
		return refusalText(err)
	}

	data, err := os.ReadFile(part)
	if err != nil {
		// The part file is gone. The destination may hold the bytes
		// already, which the controller accepts if they are the ones it
		// sent.
		data, err = os.ReadFile(dest)
		if err != nil {
			return refusalText(fmt.Errorf("the transferred file is missing"))
		}
	}

	sum := md5.Sum(data)
	got := hex.EncodeToString(sum[:])
	if len(cmd.pattern) < 8 || !isHex(cmd.pattern) || !strings.HasPrefix(got, cmd.pattern) {
		_ = os.Remove(part)
		return "VERIFY_BAD:" + got + "\n"
	}

	if _, err := os.Stat(part); err == nil {
		if err := os.Rename(part, dest); err != nil {
			return refusalText(fmt.Errorf("the credential could not be renamed: %w", err))
		}
	}

	marker := cmd.marker
	if marker == "" {
		marker = "VERIFY_OK"
	}
	return marker + "\n"
}

// resolve maps a path the controller named onto a file this device keeps.
//
// The controller names the Echo's path, and a device that keeps its
// credentials elsewhere maps it. Only the two credential files are
// reachable: a runner that could write anywhere on the device could write
// anything there.
func (s *credentialShellSession) resolve(path string) (string, string, error) {
	clean := filepath.Clean(unquotePath(path))
	part := false
	if strings.HasSuffix(clean, ".part") {
		clean = strings.TrimSuffix(clean, ".part")
		part = true
	}

	name := filepath.Base(clean)
	if name != "token" && name != "ca.pem" {
		return "", "", fmt.Errorf("%s is not a credential file", path)
	}
	matched := false
	for _, candidate := range []string{
		filepath.Join(ControllerCredentialPath, name),
		filepath.Join(s.dir, name),
	} {
		if clean == candidate {
			matched = true
			break
		}
	}
	if !matched {
		return "", "", fmt.Errorf("%s is outside the credential directory", path)
	}

	dest := filepath.Join(s.dir, name)
	if part {
		dest += ".part"
	}
	return dest, name, nil
}

// credentialMode is the mode a credential file is written with.
//
// The controller asks for a mode and it is honoured within the policy: the
// token stays readable only by the user that owns it, and the certificate
// authority stays readable by everyone and writable by nobody but its
// owner.
func credentialMode(name, requested string) os.FileMode {
	if !validFileMode(requested) {
		return defaultCredentialMode(name)
	}
	value, err := strconv.ParseUint(requested, 8, 32)
	if err != nil {
		return defaultCredentialMode(name)
	}
	mode := os.FileMode(value)
	if name == "token" {
		return mode &^ 0o077
	}
	return mode & 0o644
}

// defaultCredentialMode is the mode a credential file has when the
// controller names none.
func defaultCredentialMode(name string) os.FileMode {
	if name == "token" {
		return 0o600
	}
	return 0o644
}

// shellFields splits a command into words, honouring single quotes.
//
// The controller's commands are shell text, and a path can be quoted. A
// quoted word loses its quotes, so a path is seen the way the shell would
// see it.
func shellFields(text string) []string {
	var fields []string
	var current strings.Builder
	inWord := false
	quoted := false

	for i := 0; i < len(text); i++ {
		c := text[i]
		switch {
		case c == '\'':
			inWord = true
			quoted = !quoted
		case !quoted && (c == ' ' || c == '\t' || c == '\n' || c == '\r'):
			if inWord {
				fields = append(fields, current.String())
				current.Reset()
				inWord = false
			}
		default:
			inWord = true
			current.WriteByte(c)
		}
	}
	if inWord {
		fields = append(fields, current.String())
	}
	return fields
}

// unquotePath removes the quotes a path may be written in.
func unquotePath(path string) string {
	path = strings.TrimSpace(path)
	if len(path) >= 2 && strings.HasPrefix(path, "'") && strings.HasSuffix(path, "'") {
		return path[1 : len(path)-1]
	}
	return path
}

// stripWhitespace removes the line breaks a base64 body may be wrapped in.
func stripWhitespace(text string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\n', '\r':
			return -1
		}
		return r
	}, text)
}

// isHex reports whether a string is a hexadecimal digest.
func isHex(value string) bool {
	for _, r := range value {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}
