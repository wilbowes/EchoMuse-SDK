# Security notes

These notes describe the Go reference client's trust boundaries. They are an
engineering guide, not a conformance or security certification.

## Network links

The client has control, data and shell WebSocket planes. The token is an
authentication secret. When the controller CA is available and an encrypted
port is advertised, the SDK verifies the server certificate against that CA,
requires TLS 1.2 or later, and sends the token only over `wss`. During initial
pairing the device has no CA yet; the controller issues the token and CA via
the shell plane. A controller that has no TLS port requires an unencrypted
link, so credentials and audio are exposed to the local network. Use a
trusted network and controller in that configuration.

The SDK uses controller discovery or a configured URL; it does not treat
being on the same LAN as authentication. The administrator's controller
approval and the issued credential are the admission boundary.

## Shell plane

`LocalShell` starts `/bin/sh` (or a caller-selected path) and bridges the
controller's shell messages to it. That grants the controller the privileges
of the process running the SDK. It is required by this wire protocol for
credential transfer and maintenance operations, but it is a broad authority
boundary. Run the process as a dedicated unprivileged user. Embedded
applications should implement `ShellRunner` with an allowlist rather than
using `LocalShell`.

The credential transfer helper quotes destination paths and accepts only
octal permission strings for `chmod`. Do not pass untrusted values as shell
commands to a custom `ShellRunner`.

## Credentials

The token is stored with mode `0600`; the CA is public verification material
and is stored with mode `0644`. The credential directory is created with
mode `0700`. These permissions protect against other local users, subject to
the filesystem and operating system honoring Unix modes. Back up the
credential directory securely and do not check it into source control.

## Emulator browser UI

The UI binds to `127.0.0.1:8771` by default. Its state endpoint has no login;
button, volume, mute, and audio-device selection endpoints accept POST only
and reject requests with a cross-origin `Origin` header. If `--ui` is bound to a non-loopback
address, any network peer that can reach it can inspect state, trigger the
button, change volume/mute, or switch audio devices. Keep the listener on
loopback. If remote access is needed, restrict network access to an
authenticated reverse proxy and prevent peers from reaching the emulator port
directly; the emulator itself has no UI authentication.

The emulator's command supplies an ALSA adapter; the SDK does not depend on
ALSA. The adapter opens devices directly and closes streams when capture or
playback ends. Captured audio is forwarded to the configured controller in
stream mode. Do not run it where that behavior is inappropriate.

## Review status and remaining checks

The current source review checked credential transport and storage, shell
command construction, ALSA device access, and the local UI routes. It
found and addressed mode-string shell injection in `TransferFile`, unsafe
reuse of a broad mode on an existing token file, delayed UI bind error
reporting, and method/origin checks for the button route.

The Go client has not been independently penetration-tested or checked
against the repository's conformance suite. The default shell remains a
deliberately powerful protocol feature, and plain WebSocket mode remains
unencrypted when the controller does not offer TLS.
