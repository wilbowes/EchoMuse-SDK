# Go SDK guide

This guide describes the Go reference client in `clients/go`. The controller
wire contract and the requirements for every conforming client remain in the
[main EchoMuse repository](https://github.com/wilbowes/EchoMuse) and this
repository's [README](../README.md). The Go client has not yet passed
the conformance suite.

## What is included

The core module is `github.com/wilbowes/EchoMuse-SDK/clients/go` and
requires Go 1.24 or later. It is pure Go: it builds with `CGO_ENABLED=0`
for every platform, and it holds the protocol, the connection and the
state. The hardware is behind interfaces the application supplies.

| Package | Role |
|---|---|
| `echomuse` | Connection lifecycle, registration, callbacks, outbound messages, shell plane, credentials and system-neutral audio plugin contracts. |
| `echomuse/protocol` | JSON messages, capability names, configuration types and binary frame encoders. |
| `echomuse/state` | Thread-safe snapshots of device and link state. |
| `echomuse/display` | Converts ring frames into values a screen can render. |
| `echomuse/transport` | mDNS discovery, controller addresses, TLS configuration and credential loading. |
| `echomuse/wakeword` | Pure-Go buffering and feature pipeline around an application supplied inference backend. |
| `echomuse/vad` | Pure-Go speech detector that gates a bounded turn on speech and ends it when the speech stops. |
| `echomuse/sendspin` | Full Sendspin network player: discovery, pairing, Noise encryption, time sync, FLAC decoding, buffering and playback clock. |
| `echomuse/emulator` | Emulator lifecycle and browser UI, built on the SDK contracts. |
| `echomuse/mockcontroller` | A controller fixture used by SDK tests and local development. |

Two further modules sit beside the core, and neither is part of it:

| Module | Role |
|---|---|
| `clients/go/backends/onnxruntime` | Optional Linux+cgo ONNX Runtime backend for the openWakeWord feature models and classifier. It is one implementation of `wakeword.Inferer`; an application may bring another. |
| `clients/go/tools/emulator` | The emulator command, its ALSA adapter and the ONNX Runtime backend. It is a program, not a library. |

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
		CredentialsDir: "/var/lib/echomuse", // where this device keeps its state
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
runtime. The `backends/onnxruntime` module provides a ready ONNX Runtime
implementation for Linux amd64 and arm64. It loads `libonnxruntime.so`
dynamically and takes the melspectrogram, embedding, and classifier paths
explicitly; it does not bundle the runtime or wakeword model files. This
keeps the core SDK cross-buildable without cgo while allowing applications
to use native local inference. The backend module needs cgo, a C compiler,
and the matching runtime shared library on the target system.

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
through the shell plane. `CredentialsDir` is where the device keeps that
credential, and it is required: the SDK asks where the device's state lives
rather than guessing a path the application does not know about. The
controller's own path is mapped onto it. The emulator uses
`~/.echomuse/emulator`.

When a CA is available and the controller advertises its encrypted port, the
SDK verifies the controller certificate against that CA and sends the token
only over `wss`. A device with no CA uses plain `ws` and sends no token. Do
not set `InsecureSkipVerify`, copy credentials into logs, or share a
credential directory between devices.

By default the shell plane carries the controller's credential transfer and
nothing else: the controller can push the token and the certificate
authority, and it cannot run a command. That is the right default, because
a shell on the device is a shell on the machine the device runs on, and
under a Home Assistant add-on that is every administrator of the
installation. Set `AllowLocalShell` to expose the host's `/bin/sh`, which
the SDK logs a warning about when the shell starts, or supply a
`ShellRunner` that implements only the operations you intend to expose.
See [security review](security.md).

## Device facts and telemetry

The SDK carries the facts a device has about itself and the seams through
which an application supplies them. It does not measure anything, read any
file, or ask any operating system anything: an application that knows its
hardware fills the seams in, and the SDK puts them on the wire.

Everything here follows one rule: **a missing measurement is absent, never
zero.** The controller stores an absent measurement as NULL and a measured
zero as a number, and those are different answers. A device that cannot
read its own temperature reports no temperature; it does not report 0 °C.

### What the device declares at registration

| Field | What it is |
|---|---|
| `Version` | The application's own version. |
| `FirmwareVersion` | The firmware version the controller compares against the published releases. Empty means `Version`. |
| `Board` | The hardware id. The controller names the model in Home Assistant from it. |
| `BootID` | The kernel's `/proc/sys/kernel/random/boot_id`. The controller keeps one row per boot, so this is what tells a restart from a reconnect. |
| `BootReason` | Why the device started. |
| `KernelArch`, `KernelRelease` | The running kernel's `uname -m` and `uname -r`. |
| `EMMC` | The flash wear report. The keys the controller reads are `rev`, `preEol`, `lifeA`, `lifeB`, `name`, `date` and `manfid`. |

```go
device, err := echomuse.New(echomuse.Options{
	DeviceID:       "panel-0001",
	Version:        "0.1.0",
	FirmwareVersion: "v999.0.0",   // the controller will not offer an update
	Board:          "home-panel",
	BootID:         readBootID(),
	BootReason:     "power_key",
	KernelArch:     "aarch64",
	KernelRelease:  "6.1.0",
	EMMC: map[string]any{
		"rev": 7, "preEol": 1, "lifeA": 1, "lifeB": 2,
		"name": "FJ25AB", "date": "08/2017", "manfid": "0x000015",
	},
	Capabilities:   []protocol.Capability{protocol.CapMic, protocol.CapSpeaker},
	ControllerURL:  controllerURL,
	CredentialsDir: credentialsDir,
})
```

**`FirmwareVersion` and `Version` are separate on purpose.** The controller
compares the version a device reports against the published firmware
releases and offers an update when the device is behind. A device the
controller cannot update — a panel, a box on a desk, anything that is not an
Echo — still has to answer that question, and the honest answer is the
version it is on. A device that leaves `FirmwareVersion` empty reports
`Version`, which is right for a device whose firmware *is* the application.

### The telemetry

`StatsSource` is called on a ticker and its reading is sent as the stats
message. It is a source and not a report: the SDK does not know what a CPU
percentage is.

```go
device, err := echomuse.New(echomuse.Options{
	// ...
	StatsInterval: 30 * time.Second,
	StatsSource: func() protocol.Stats {
		return protocol.Stats{
			CPUPct:        cpuShare(),
			MemUsedMb:     memoryUsed(),
			MemTotalMb:    memoryTotal(),
			StorageUsedMb: storageUsed(),
			StorageTotalMb: storageTotal(),
			WifiRssi:      signalStrength(),   // a pointer: nil means not measured
			WifiSsid:      "home",
			LinkSpeedMbps: linkSpeed(),
			CPUTempC:      cpuTemperature(),
			CoresOnline:   &coresOnline,
			CoresTotal:    &coresTotal,
			EMMC:          flashWear(),
		}
	},
})
```

The pointer fields are the ones where zero is a real reading: a signal
strength of 0 dBm is a different answer from no signal reading at all, and
the controller shows one as "no signal" and the other as a number.

`Device.Stats()` returns the current reading without waiting for the tick,
which is what an application wants for its own display.

The full set of fields is in `protocol.Stats`. `EMMC` rides there too, so
the flash wear is re-read on the device every few hours and the controller
stores one row per day on a change.

### The microphone mute

Mute is **device-sovereign**. The device holds the authority and the
controller is told; there is no controller-to-device mute command and there
is not going to be one.

```go
// The device's own switch changed.
if err := device.SendMicMute(muted); err != nil {
	log.Error("the mute could not be reported", "error", err)
}
```

`SendMicMute` reports the state to the controller and calls the
`OnMicMute` handlers, so an application that drives its own switch has one
call to make and one place to be told.

```go
device.OnMicMute(func(muted bool) {
	// The device's own state has changed. Stop or open the capture.
})
```

This is the **microphone**'s mute. The controller mutes a device's
*output* by sending a volume of zero, which is a different thing entirely
and arrives as `OnVolumeSet`.

### The network

The controller can ask a device to scan for networks and to join one. The
SDK carries the exchange and the application answers it.

```go
device.OnWifiScan(func() {
	// The controller wants to know what is in range.
	networks, err := scanForNetworks()
	if err != nil {
		// Say so. The controller waits for this answer and gives up
		// after a timeout, and a silent device cannot be told apart
		// from a busy one.
		_ = device.SendWifiScanResult(protocol.WifiScanResult{Error: err.Error()})
		return
	}
	_ = device.SendWifiScanResult(protocol.WifiScanResult{Networks: networks})
})

device.OnWifiChange(func(change protocol.WifiChange) {
	// The controller wants the device on another network.
	err := joinNetwork(change.SSID, change.PSK)
	result := protocol.WifiResult{SSID: change.SSID}
	if err != nil {
		result.Error = err.Error()
	} else {
		result.OK = true
	}
	_ = device.SendWifiResult(result)
})

device.OnWifiCommit(func() {
	// The controller has recorded the outcome. The device can stop
	// repeating its result.
})
```

The result is re-sent until the commit arrives, because a network change
kills the connection the answer would have travelled on. An SSID is 0–32
arbitrary bytes, so `SSIDHex` is the addressable form and it is what the
controller sends back for a network that came from a scan.

The PSK is a secret. It arrives over the control plane and an application
that logs its messages must not log this one.

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

The SDK packages build without cgo. The emulator command also compiles without
cgo, but its audio adapter is then unavailable. On Linux, audio requires cgo,
a C compiler at build time, and `libasound.so.2` at run time. The optional
The `backends/onnxruntime` module separately uses cgo and a target-matched
`libonnxruntime.so`. The SDK itself does not depend on ALSA. See
[run the emulator](emulator.md).
