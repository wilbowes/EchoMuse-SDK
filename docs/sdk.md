# Go SDK guide

This guide describes the Go reference client in `clients/go`. The controller
wire contract and the requirements for every conforming client remain in the
[main EchoMuse repository](https://github.com/wilbowes/EchoMuse) and this
repository's [README](../README.md). The Go client has not yet passed
the conformance suite.

## What is included

The module is `github.com/wilbowes/EchoMuse-SDK/clients/go` and requires Go
1.24 or later.

| Package | Role |
|---|---|
| `echomuse` | Connection lifecycle, registration, callbacks, outbound messages, shell plane, credentials and system-neutral audio plugin contracts. |
| `echomuse/protocol` | JSON messages, capability names, configuration types and binary frame encoders. |
| `echomuse/state` | Thread-safe snapshots of device and link state. |
| `echomuse/display` | Converts ring frames into values a screen can render. |
| `echomuse/transport` | mDNS discovery, controller addresses, TLS configuration and credential loading. |
| `echomuse/wakeword` | Pure-Go buffering and feature pipeline around an application supplied inference backend. |
| `echomuse/wakeword/ort` | Optional Linux+cgo ONNX Runtime backend for the openWakeWord feature models and classifier. |
| `echomuse/sendspin` | Full Sendspin network player: discovery, pairing, Noise encryption, time sync, FLAC decoding, buffering and playback clock. |
| `echomuse/emulator` | Emulator lifecycle and browser UI, built on the SDK contracts. |
| `echomuse/mockcontroller` | A controller fixture used by SDK tests and local development. |

The emulator command lives in `cmd/emulator`.

## Connection lifecycle

Create a `Device`, register callbacks, and call `Run` with a context. `Run`
discovers a controller by mDNS unless `ControllerURL` is supplied, registers
the device, opens the control and data planes, handles reconnects, and exits
when the context is cancelled or `Stop` is called.

```go
package main

import (
	"context"
	"log"

	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse"
	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/protocol"
	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/state"
)

func run(ctx context.Context) error {
	device, err := echomuse.New(echomuse.Options{
		DeviceID: "kitchen-panel-01", // persist a stable ID for this device
		Version: "0.1.0",
		Board: "custom-panel",
		Capabilities: []protocol.Capability{
			protocol.CapMic,
			protocol.CapSpeaker,
			protocol.CapPairing,
		},
		ControllerURL: "ws://192.168.1.10:8767", // omit for mDNS
	})
	if err != nil {
		return err
	}

	device.OnMicStart(func(lockMic bool) {
		// Start capture in a goroutine. Callbacks run on a protocol reader;
		// blocking here would stop it from processing controller messages.
		go captureAndSend(ctx, device, lockMic)
	})
	device.OnMicStop(stopCapture)
	device.OnSpeaker(func(pcm []byte) { playPCM(pcm) })
	device.OnSpeakerEOS(stopPlayback)
	device.State().OnChange(func(s state.Snapshot) {
		log.Printf("phase=%s listening=%s", s.Phase, s.Listen)
	})
	return device.Run(ctx)
}
```

`captureAndSend`, `playPCM`, `stopCapture`, and `stopPlayback` above represent
hardware-specific functions supplied by the application. `lockMic` indicates
a bounded, speech-gated turn. Without it, the controller has requested its
continuous microphone stream. The SDK does not capture audio or resample it.

## Audio contract

- Microphone and private-session audio: signed 16-bit little-endian mono at
  16 kHz.
- Speaker and music audio: signed 16-bit little-endian mono at 48 kHz.
- The SDK sends each provided byte slice as one frame; callers should use
  sensible chunks and must not mutate a slice until the send returns.
- The SDK does not resample, apply gain, or provide a hardware driver.

## Listening modes and privacy

The client exposes both continuous-stream and device-local wake APIs. For a
private client, keep audio on the device while scoring the wake word, report
`protocol.ListenLocal`, score with `wakeword.NewListener` and an application-
supplied local inference backend, then call `StartListenSession` only when the
trigger fires. It assigns a random session id; send buffered pre-roll and later
audio with `SendListenAudio`. Handle acknowledgements and controller closure
through `OnListenAck` and `OnListenClose`; the SDK closes a session after the
protocol's 3-second acknowledgement deadline or 30-second maximum. End earlier
when local VAD, mute, or policy requires it with `EndListenSession`. The SDK owns framing and session ids; the application owns
capture, model loading/runtime, pre-roll policy, and stopping capture after a
close. Do not send microphone data to a remote inference service in local mode.

`wakeword.Inferer` isolates the streaming feature pipeline from the model
runtime. `wakeword/ort` provides a ready ONNX Runtime implementation for Linux
amd64 and arm64. It loads `libonnxruntime.so` dynamically and takes the
melspectrogram, embedding, and classifier paths explicitly; it does not bundle
the runtime or wakeword model files. This keeps the core SDK cross-buildable
without cgo while allowing applications to use native local inference. The
optional package needs cgo, a C compiler, and the matching runtime shared
library on the target system.

## Sendspin and telemetry

Devices implementing Sendspin should announce `protocol.CapSendspin`, handle
`OnSendspinTokenRequest` by obtaining the current token and answering with
`SendSendspinToken`, and report player changes through `SendSendspinStatus`.
Treat the token as a secret: fetch it when requested and never log it.
`SendPlaybackStats` reports one drained response stream, while `SendStats`
accepts optional hardware and link measurements. Pointer fields distinguish a
real zero from a value the hardware cannot measure.

The `echomuse/sendspin` package is the complete player implementation used by
the emulator. Enable it from `protocol.Config.SendspinEnabled`, persist its
identity under a private directory, and route its stereo `Fill` periods to a
clocked audio output. Set `Unpaired` only when the operator has explicitly
allowed unpaired servers; pairing is the normal path.

If the device cannot run a local wake model, the repository policy requires
button-only operation; it must not silently fall back to continuous
streaming. `ListenDegraded` is the honest status for that case. The SDK's
emulator is a development demonstration that intentionally uses controller
streaming and is not a conforming client.

The `wakeword` package accepts an `Inferer`; it contains no bundled model or
inference runtime. The application supplies the melspectrogram, embedding and
classification implementation. A `Detector` belongs to one audio goroutine,
accepts 16 kHz PCM through `Push`, and returns `ErrNotReady` during warm-up.
`NewTrigger` can report only threshold crossings and suppress repeated
crossings. See package comments and the types in `echomuse/wakeword` for
buffer dimensions and callback behavior.

## Pairing and credentials

An unpaired device can call `StartPairing`; the controller's dashboard
administrator approves it. The controller then sends the link token and CA
through the shell plane. `CredentialsDir` controls where the SDK reads them;
without it the SDK uses the controller's default path. The emulator uses
`~/.echomuse/emulator`.

When a CA is available and the controller advertises its encrypted port, the
SDK verifies the controller certificate against that CA and sends the token
only over `wss`. A device with no CA uses plain `ws` and sends no token. Do
not set `InsecureSkipVerify`, copy credentials into logs, or share a
credential directory between devices.

The default `LocalShell` gives the controller a real `/bin/sh` session. This
is a broad remote-control boundary. Embedded applications should supply a
restricted `ShellRunner` that implements only the operations they intend to
expose. See [security review](security.md).

## Capabilities and state

Capabilities are declarations of implemented behavior, not feature requests.
Only announce a capability if the hardware and application implement it.
After registration, `HasFeature` reports controller features negotiated in
the acknowledgement.

`State().Snapshot()` returns a copy that is safe to read across goroutines.
`OnChange` runs when that snapshot changes. Missing information stays
unknown; applications should not treat unknown as false or zero. The display
tracker similarly turns LED frames into a `View`, but does not draw it.

## Build and run checks

From the repository root:

```sh
cd clients/go
go test -race ./...
go vet ./...
```

The SDK's core, Sendspin player and emulator build without cgo. The optional
`wakeword/ort` package uses cgo and a target-matched `libonnxruntime.so`. The
emulator command supplies a Linux ALSA adapter and requires cgo plus
`libasound.so.2`; the SDK itself does not depend on ALSA. See
[run the emulator](emulator.md).
