# EchoMuse Go client

This directory contains the Go protocol client, its device packages and a
development emulator. The client implements the device side of the EchoMuse
controller interface.

The repository's listening rules apply to every conforming client. The
emulator streams microphone audio to the controller and is a development
tool; it does not conform. The library has private-listening support, but it
has not yet been checked against the conformance suite.

## Build

Go 1.24 or later is required.

```sh
go test ./...
go vet ./...
go run ./cmd/emulator
```

The emulator command uses direct ALSA audio on Linux when built with cgo; a
no-cgo build has no audio backend. It also uses a POSIX `/bin/sh` for
credential transfer. See
[`../../docs/emulator.md`](../../docs/emulator.md) for requirements, options and the
pairing flow; [`../../docs/sdk.md`](../../docs/sdk.md) for packages and usage; and
[`../../docs/security.md`](../../docs/security.md) for the trust boundaries. The existing
[getting-started guide](../../docs/getting-started.md) and
[protocol notes](../../docs/protocol.md) provide further detail.

The module is `github.com/wilbowes/EchoMuse-SDK/clients/go`. The repository
root [LICENSE](../../LICENSE) applies; third-party notices are in
[`NOTICE.md`](NOTICE.md).
