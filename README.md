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

Status: just started. Nothing here is usable yet.

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

## Licence

MIT. See [LICENSE](LICENSE).
