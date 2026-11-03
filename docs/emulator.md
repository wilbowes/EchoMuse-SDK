# Run the emulator

The emulator is a development device for trying the Go client against a
controller. It pairs through the controller dashboard, uses the computer's
microphone and speaker, and shows link and ring state in a browser window.
It streams microphone audio to the controller, so it does not meet the
repository's private-listening requirement and is not a conforming client.

## Requirements

- Go 1.24 or later.
- Linux with cgo enabled and the ALSA runtime library (`libasound.so.2`) for
  microphone and speaker I/O.
- A POSIX `/bin/sh`, which the default shell plane uses for controller
  credential transfer.
- A reachable EchoMuse controller. mDNS discovery requires the computer and
  controller to share a network that passes multicast DNS; otherwise supply
  `--controller`.
- A controller administrator who can approve the emulator in the dashboard.

## Start

From the repository root:

```bash
cd clients/go
go run ./cmd/emulator
```

The UI listens on `127.0.0.1:8771` by default and opens in the browser. The
emulator discovers the controller using mDNS, opens a pairing request, and
retries while waiting. Approve the device from the controller dashboard. It
then receives its link credentials over the shell plane and reconnects.
Use Ctrl+C to stop it. The credential files remain in
`~/.echomuse/emulator` so later runs can reconnect as the same device.

To bypass mDNS, specify the controller URL:

```bash
go run ./cmd/emulator --controller ws://192.168.1.10:8767
```

Cross-build the pure-Go command for Linux amd64 and Raspberry Pi arm64 without
a C toolchain:

```bash
cd clients/go
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o dist/echomuse-emulator-linux-amd64 ./cmd/emulator
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o dist/echomuse-emulator-linux-arm64 ./cmd/emulator
```

For 32-bit Raspberry Pi OS, use `GOARCH=arm GOARM=7` on ARMv7 hardware. Copy
the executable to the target; install ALSA runtime support there for audio.

## Local wakeword scoring

The emulator can score a wakeword locally using the SDK's ONNX Runtime
backend. It does not stream mic audio while the controller configures
`owwOnDevice=on`; after a crossing it sends a short pre-roll and session audio.
The controller still chooses the model and threshold. The classifier filename
must match the controller model identifier, and the shared feature models,
classifier, and runtime library must be installed for the target architecture.

```bash
go run ./cmd/emulator \
  --wakeword \
  --onnxruntime /usr/lib/libonnxruntime.so \
  --wakeword-model-dir ./models/oww \
  --wakeword-classifier hey_jarvis_v0.1.onnx
```

This optional backend requires cgo, a C compiler, and a Linux ONNX Runtime
shared library. To build an emulator with it enabled, compile on the target or
use the matching cross C compiler and `CGO_ENABLED=1`. The default
`CGO_ENABLED=0` SDK build includes a stub that reports the missing runtime and
keeps the rest of the SDK portable. Wakeword models and ONNX Runtime are not
bundled with the SDK.

## Sendspin player

The emulator advertises Sendspin support. Enable Sendspin for the device in
the controller config; the player then advertises by mDNS, accepts paired
servers with Noise authentication, reports player state, and answers pairing
token requests. The identity and pairing records are stored in
`<credentials-dir>/sendspin.json` with private file permissions. The default
mode requires pairing. Keep TCP port 8928 reachable from the controller and
Music Assistant only on the intended LAN.

The command's ALSA adapter handles voice capture and playback. The browser can
switch microphones and speakers while it is running. Sendspin uses the same
playback backend as voice by default, so it does not open a second handle that
can reserve the speaker. Applications that can report DAC timing may inject an
optional clocked output backend. A Linux cgo build requires a C compiler and
the `libasound.so.2` runtime library; `CGO_ENABLED=0` does not provide audio
I/O.

To run without audio capture or without the browser UI:

```bash
go run ./cmd/emulator --mute-capture
go run ./cmd/emulator --no-window
```

`--mute-capture` suppresses microphone transmission for that run. The emulator
still registers microphone support and may report the controller's stream
mode; use it as a demo control, not as the private-listening guarantee for a
real client.

## Options

| Flag | Default | Purpose |
|---|---|---|
| `--controller URL` | mDNS | Controller base URL, such as `ws://192.168.1.10:8767`. |
| `--controller-tls-port N` | `0` | Encrypted WebSocket port when using a fixed controller address. |
| `--device-id ID` | machine hostname | Stable identity shown by the controller. |
| `--credentials DIR` | `~/.echomuse/emulator` | Directory for token and CA files. Keep it private and persistent. |
| `--ui ADDRESS` | `127.0.0.1:8771` | Browser UI bind address. |
| `--audio-input DEVICE` | system default | Microphone device name. |
| `--audio-output DEVICE` | system default | Speaker device name. |
| `--list-audio-devices` | off | List available audio devices and exit. |
| `--mute-capture` | off | Do not send microphone audio. |
| `--no-window` | off | Do not start the HTTP UI or open a browser. |
| `--log-level LEVEL` | `info` | `debug`, `info`, `warn` or `error`. |

List devices first if the default audio devices are wrong:

```bash
go run ./cmd/emulator --list-audio-devices
go run ./cmd/emulator --audio-input alsa:plughw:2,6 --audio-output alsa:plughw:2,0
```

The UI exposes device state and a button endpoint without authentication. Keep
the default loopback bind. Binding `--ui` to a LAN or public interface lets
other reachable users inspect that state and press the emulator's action
button. The UI does not expose a general controller credential endpoint, but
the device itself serves the controller's shell protocol for credential issue;
run the emulator only on a network/controller you trust.

The window also provides a 0–100 speaker volume slider and a microphone mute
toggle. Volume scales playback PCM and reports changes to the controller. Mute
closes capture and reports the device-owned mute state; while muted, a new
controller microphone request is ignored.

---

## What it is for

Three things, in order of how often they come up.

**Seeing the SDK work against a real controller.** The window narrates
every step, so a failure says which step failed rather than just that
something did.

**Building an application against something that answers.** An application
written for a custom device can be developed against the emulator, on a
machine with no custom hardware, and then pointed at the real thing.

**Being the thing in the field.** A device that is a screen on a wall, or a
speaker with a microphone, and nothing else, is an Echo for a different
purpose. The emulator is what that looks like when the hardware is a
laptop.

## The window

The window is a page served on the loopback address, opened in the
browser. It is a web page rather than a native window because the SDK is
pure Go with no cgo, and a native window would mean a toolchain dependency
on every machine that runs it.

The styling follows the EchoMuse dashboard: the same warm-grey ground, the
same dark inset LCD readouts, the same colours for the device states. An
emulator that looked like a different product would be harder to trust
than one that looks like the thing it stands in for.

It shows:

- **the ring** — the controller's twelve indicators, drawn on a circle at
  their real positions;
- **the phase** — what the device is doing, in the middle of the ring;
- **the readouts** — the link, the admission, the credential, the ring's
  pattern;
- **the walkthrough** — each step of joining the controller, and whether it
  is done.

## The flow it walks through

| Step | Who does it |
|---|---|
| Find the controller | The emulator, by mDNS or by the address given |
| Ask to be admitted | The emulator, with the pairing flag in its registration |
| Approve the device | **A person, in the controller's dashboard** |
| Get the link credential | The controller, pushing it over the shell plane |
| Join the encrypted link | The emulator, on its next redial |

The approval is the one human decision, and it is not the emulator's to
make. That is the point: the flow is the real one, so an application
developed against the emulator meets the same steps it will meet against a
live controller.

### The pairing window

The emulator opens a pairing window before its first dial, so the very
first registration already carries the request. The request repeats every
five seconds for as long as the window is open, which is what keeps it on
the dashboard — the controller drops a request 30 seconds after its last
repeat.

A device that had never asked would arrive as **pending**, which the
dashboard shows as an ordinary device waiting for approval. A device that
asks arrives as **pairing**, and the dashboard offers the approval
directly.

## The audio

Real. The microphone is the machine's microphone and the speaker is the
machine's speaker, so a voice turn through Home Assistant works end to
end.

The emulator command supplies a Linux ALSA adapter for voice capture and playback. The SDK exposes a system-neutral audio backend interface, so other applications can supply their own audio system. The browser lets you switch ALSA microphones and speakers live; device names are shown in full. Sendspin shares the same speaker stream so it does not hold the device separately from voice audio.

The rates are the protocol's, and they differ: **capture is 16 kHz** and
**playback is 48 kHz**, both signed 16-bit little-endian mono. ALSA plug devices handle format conversion.

`--mute-capture` runs the flow without sending the microphone to the
controller, which is what to use in a room where the sound should not
leave.

### Choosing the microphone and the speaker

A machine with one sound card needs no choice. A machine with several —
a laptop with a built-in microphone, a webcam and a headset — does, and
the system default is rarely the one a person wants.

```bash
go run ./cmd/emulator --list-audio-devices
```

```
Microphones:
  alsa:hw:00,00	USB
  alsa:hw:02,06	DMIC
  alsa:hw:02,07	DMIC16kHz

Speakers:
  alsa:hw:02,00	HDA
  alsa:hw:02,31	HDA
```

The name goes to `--audio-input` or `--audio-output`:

```bash
go run ./cmd/emulator --audio-input alsa:hw:02,06 --audio-output alsa:hw:02,00
```

A device is named by the server it belongs to and the device within it,
written `server:name`. The server is the part that matters:

| Name | Reaches |
|---|---|
| `alsa:hw:2,0` | An ALSA card and device, by number |
| `default` | The system default for that direction |

Device names are ALSA PCM names. The browser shows the full card and device descriptions alongside each name.

The **microphone meter** in the window's footer answers the question this
exists for: is the microphone open, and is it hearing anything. A bar that
moves means audio is going to the controller. A bar that sits at zero
while the controller says it is listening means the microphone is open and
the room is silent, or the chosen device is the wrong one.

## What it announces

```go
mic, speaker, buttons, button_hold, pairing
```

Not `leds` or `led_anim`: it has a window rather than a ring, and it draws
the state rather than the pixels. Announcing a ring it does not have would
put a control on the dashboard that does nothing.

## Using it as a library

The emulator package is separate from its Linux ALSA adapter. Applications
can provide any implementation of the SDK audio contract:

```go
emu, err := emulator.New(emulator.Options{
	ControllerURL:  "ws://192.168.1.10:8767",
	DeviceID:       "my-device",
	CredentialsDir: "/var/lib/echomuse",
	Audio:          audio.NewBackend("", ""),
})

// The window's handler, for a server of your own.
http.Handle("/", emulator.NewHandler(emu))
```

The pieces it is made of are separable:

- `echomuse.AudioBackend` — the system-neutral PCM contract. The command
  supplies an ALSA adapter; applications can inject an implementation for
  their own operating system and audio stack.
- `echomuse.Device` — the client underneath. Everything the emulator does
  is available without it.
- `display.Tracker` — the ring, as values.

## Testing against it

The mock controller is the fixture for tests, and it models the parts of
the controller that matter: the three planes, the registration and its
three answers, the configuration push, the ring frames, and the credential
issue over the shell plane.

```go
mock := mockcontroller.New(mockcontroller.Options{
	Mode: mockcontroller.ModePending,
})
defer mock.Close()

emu, err := emulator.New(emulator.Options{
	ControllerURL: mock.URL(),
	Audio:         silentAudio{},
})
```

The mock holds a pending device the way the controller does — by closing
the connection — so a device's retry against the mock is the same retry it
would make against the real thing.

`mock.Approve()` is the administrator's action, and `mock.IssueCredential`
is the controller's answer to it.
