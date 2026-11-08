package echomuse

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/protocol"
)

// TestTheStateDirectoryIsRequired checks that a device says where its
// state lives.
//
// The controller issues the link credential over the shell plane, and the
// device has to know where to put it. A library that guessed a path would
// write the credential somewhere the application does not look, and the
// device would stay on the plain link for ever — so the question is asked
// rather than answered.
func TestTheStateDirectoryIsRequired(t *testing.T) {
	_, err := New(Options{
		DeviceID:     "no-state-dir",
		Capabilities: []protocol.Capability{protocol.CapMic},
	})
	if err == nil {
		t.Fatal("the client was built without a state directory")
	}
	if !strings.Contains(err.Error(), "credentials directory") {
		t.Errorf("the error was %q, want it to name the missing directory", err)
	}
}

// TestAClientWithNoOptionsRefusesAGeneralCommand checks the default.
//
// A client built with no options serves the shell plane, because it has
// to: the controller issues the link credential over it. But it does not
// run the controller's commands. An operator who can open the controller's
// shell tab would otherwise have a shell on the device, as the user
// running the client, and under a Home Assistant add-on that is every
// administrator of the installation.
//
// The refusal is in the session's output rather than in the write's
// error, because that is where the controller reads it from.
func TestAClientWithNoOptionsRefusesAGeneralCommand(t *testing.T) {
	device, err := New(Options{
		DeviceID:       "no-options-device",
		Capabilities:   []protocol.Capability{protocol.CapMic},
		CredentialsDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("building the client: %v", err)
	}

	session, err := device.defaultShellRunner().Start(context.Background(), false)
	if err != nil {
		t.Fatalf("starting the default shell runner: %v", err)
	}
	defer session.Close()

	for _, command := range []string{
		"id\n",
		"cat /etc/passwd\n",
		"uname -a\n",
	} {
		answer := askSession(t, session, command)
		if !strings.Contains(answer, "refused") {
			t.Errorf("the answer to %q was %q, want a refusal", strings.TrimSpace(command), answer)
		}
	}

	// A command with a side effect must leave nothing behind.
	marker := filepath.Join(t.TempDir(), "pwned")
	askSession(t, session, "echo pwned > "+marker+"\n")
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Error("the default shell runner ran a command that wrote a file")
	}
}

// TestTheDefaultShellRunnerRefusesToWriteElsewhere checks that the
// runner's reach is the credential directory and nothing else.
//
// The transfer is answered only for the two files that make up the link
// credential, and only when the controller names them. A path of the
// controller's choosing cannot become a path on the device.
func TestTheDefaultShellRunnerRefusesToWriteElsewhere(t *testing.T) {
	dir := t.TempDir()
	runner := credentialShellRunner{dir: dir}

	session, err := runner.Start(context.Background(), false)
	if err != nil {
		t.Fatalf("starting the session: %v", err)
	}
	defer session.Close()

	elsewhere := filepath.Join(t.TempDir(), "authorized_keys")
	push := controllerPush(elsewhere, []byte("ssh-rsa AAAA\n"), "600")

	answer := askSession(t, session, push)
	if !strings.Contains(answer, "refused") {
		t.Errorf("the answer to a push outside the credential directory was %q, want a refusal", answer)
	}
	if _, err := os.Stat(elsewhere); !os.IsNotExist(err) {
		t.Error("the runner wrote a file outside the credential directory")
	}
}

// TestTheOptInGivesTheHostShell checks that the host shell is there when
// it is asked for.
//
// The opt-in is the way a device that is a general-purpose computer gets
// the shell the protocol can carry, and it is the only way.
func TestTheOptInGivesTheHostShell(t *testing.T) {
	device, err := New(Options{
		DeviceID:        "opt-in-device",
		Capabilities:    []protocol.Capability{protocol.CapMic},
		CredentialsDir:  t.TempDir(),
		AllowLocalShell: true,
	})
	if err != nil {
		t.Fatalf("building the client: %v", err)
	}

	runner := device.defaultShellRunner()
	if _, ok := runner.(LocalShell); !ok {
		t.Fatalf("the opt-in runner is a %T, want the host shell", runner)
	}
}

// controllerPush is the command the controller writes to push a file.
//
// It is the shape of the real controller's push: the heredoc, then the
// mode and the confirmation on the line after it.
func controllerPush(dest string, data []byte, mode string) string {
	encoded := base64.StdEncoding.EncodeToString(data)
	return "base64 -d > " + dest + ".part << 'EMSDK_B64_EOF'\n" +
		encoded + "\nEMSDK_B64_EOF\n" +
		"chmod " + mode + " " + dest + ".part && echo TRANSFER_OK\n"
}

// controllerVerify is the command the controller writes to check a file it
// pushed.
func controllerVerify(dest string, data []byte) string {
	sum := md5.Sum(data)
	want := hex.EncodeToString(sum[:])
	return "GOT=$(md5sum " + dest + ".part 2>/dev/null | cut -d' ' -f1); " +
		"case \"$GOT\" in " + want + "*) mv " + dest + ".part " + dest + " && echo VERIFY_OK ;; " +
		"*) rm -f " + dest + ".part; echo \"VERIFY_BAD:$GOT\" ;; esac\n"
}

// TestCredentialShellAcceptsTheControllersPush checks that the default
// runner carries the credential transfer the controller does.
//
// The transfer is the one thing the shell plane is needed for, and the
// device is worthless without it: no credential, no encrypted link.
func TestCredentialShellAcceptsTheControllersPush(t *testing.T) {
	dir := t.TempDir()
	runner := credentialShellRunner{dir: dir}

	session, err := runner.Start(nil, false)
	if err != nil {
		t.Fatalf("starting the session: %v", err)
	}
	defer session.Close()

	token := []byte("a-link-token\n")
	ca := []byte("-----BEGIN CERTIFICATE-----\nca\n-----END CERTIFICATE-----\n")

	dest := ControllerCredentialPath + "/token"
	if _, err := session.Write([]byte(controllerPush(dest, token, "600"))); err != nil {
		t.Fatalf("the transfer was refused: %v", err)
	}

	answer := readAnswer(t, session)
	if !strings.Contains(answer, "TRANSFER_OK") {
		t.Errorf("the transfer was answered with %q, want it to confirm", answer)
	}

	if _, err := session.Write([]byte(controllerVerify(dest, token))); err != nil {
		t.Fatalf("the verification was refused: %v", err)
	}

	answer = readAnswer(t, session)
	if !strings.Contains(answer, "VERIFY_OK") {
		t.Errorf("the verification was answered with %q, want it to confirm", answer)
	}

	got, err := os.ReadFile(filepath.Join(dir, "token"))
	if err != nil {
		t.Fatalf("reading the token: %v", err)
	}
	if string(got) != string(token) {
		t.Errorf("the token was %q, want %q", got, token)
	}

	// The certificate authority is pushed the same way.
	caDest := ControllerCredentialPath + "/ca.pem"
	if _, err := session.Write([]byte(controllerPush(caDest, ca, "644"))); err != nil {
		t.Fatalf("the transfer was refused: %v", err)
	}
	readAnswer(t, session)

	if _, err := session.Write([]byte(controllerVerify(caDest, ca))); err != nil {
		t.Fatalf("the verification was refused: %v", err)
	}
	readAnswer(t, session)

	got, err = os.ReadFile(filepath.Join(dir, "ca.pem"))
	if err != nil {
		t.Fatalf("reading the certificate authority: %v", err)
	}
	if string(got) != string(ca) {
		t.Errorf("the certificate authority was %q, want %q", got, ca)
	}
}

// readAnswer reads one answer from the session.
func readAnswer(t *testing.T, session ShellSession) string {
	t.Helper()
	buf := make([]byte, 4096)
	n, err := session.Read(buf)
	if err != nil {
		t.Fatalf("reading the answer: %v", err)
	}
	return string(buf[:n])
}

// runCommand writes a command and reads the answer to it.
//
// The answer is read with a deadline, so a command the runner does not
// answer at all fails the test rather than hanging it.
func askSession(t *testing.T, session ShellSession, command string) string {
	t.Helper()
	if _, err := session.Write([]byte(command)); err != nil {
		t.Fatalf("writing %q: %v", strings.TrimSpace(command), err)
	}

	type result struct {
		answer string
		err    error
	}
	done := make(chan result, 1)
	go func() {
		buf := make([]byte, 4096)
		n, err := session.Read(buf)
		done <- result{answer: string(buf[:n]), err: err}
	}()

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("reading the answer to %q: %v", strings.TrimSpace(command), r.err)
		}
		return r.answer
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for the answer to %q", strings.TrimSpace(command))
		return ""
	}
}
