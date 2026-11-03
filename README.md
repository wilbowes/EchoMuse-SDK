# EchoMuse SDK

Make any app or device an EchoMuse client.

An EchoMuse client talks to the EchoMuse controller the same way a repurposed
Echo does. This repository will hold three things:

1. **The protocol spec**, taken from
   [`docs/device-controller-interface.md`](https://github.com/wilbowes/EchoMuse/blob/main/docs/device-controller-interface.md)
   in the main repo.
2. **A conformance suite** that any client can run to prove it follows the spec,
   including the listening rules below.
3. **Reference client libraries** that pass that suite.

The protocol spec and conformance suite are still being built. A Go client
implementation is available under [`clients/go`](clients/go/README.md); it has
not yet been checked against the conformance suite.

## The listening rules hold for every client

EchoMuse is private by default
([`docs/listening.md`](https://github.com/wilbowes/EchoMuse/blob/main/docs/listening.md)).
A client built with this SDK has to keep the same promises an Echo does:

- **No audio leaves before the client's own wake word fires** (or a button
  press), and it stops at the end of speech.
- **The client enforces its own limits** (ack timeout, maximum session length,
  mute, link loss), so a controller fault can never leave it streaming.
- **Nothing falls back to streaming.** A client that cannot run its own wake
  word says so and works by button only.
- **What it reports about listening is what it is actually doing**, never
  what it was configured to do.

The conformance suite tests these. A client that fails them is not an
EchoMuse client.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Faults with the controller or an Echo
belong in the [main repo](https://github.com/wilbowes/EchoMuse/issues).

## Reference clients

- [Go](clients/go/README.md) — protocol client, device packages and emulator.
  The emulator demonstrates controller streamed listening, which does not
  meet this repository's private-listening requirement. It is a development
  tool, not a conforming client. The library's conformance status is pending
  the suite. Read the [Go SDK guide](docs/sdk.md),
  [emulator instructions](docs/emulator.md), and
  [security notes](docs/security.md).

## Licence

MIT. See [LICENSE](LICENSE).
