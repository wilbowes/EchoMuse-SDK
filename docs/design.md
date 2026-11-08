# Design notes

The decisions behind the API, and the things it deliberately does not do.
Most of this is a record of why, because the reasons are the part that is
easy to lose.

---

## The SDK is a protocol client, not a framework

The Echo firmware is not a library you import. It is a program that runs on
an Echo, and it owns that Echo's hardware, its state file, its boot, and
its own wake word.

The SDK is the other shape: a library your program imports, which owns the
protocol and nothing else. Your application owns the hardware and the
interface.

That boundary is what makes the SDK usable on a Raspberry Pi. The
alternative — a "device runtime" with its own main loop, its own config
file and its own lifecycle — would be a program, and you would be writing a
plugin for it rather than a program that uses it.

### What that means in the API

- There is no `Main`. There is a `Device` and a `Run`.
- There is no configuration file. The application builds an `Options` struct.
- There is no global state. Everything is reachable from the `Device`.
- There is no application lifecycle. The SDK does not decide when your
  program starts or stops.

## The state tracker exists so applications never parse the protocol

The controller's status surface is a ring of twelve indicators, and its
messages are about pixels: a colour, a pattern, a flag. A device with a
screen has a different vocabulary — it wants to know whether it is
listening, thinking, speaking or muted.

So the SDK keeps two things, and they are different:

- **`state.Tracker`** — the coarse answer. What phase is the device in, is
  it muted, is the link up, is it approved, what did the controller
  announce it can do. Derived from the messages that actually arrive, and
  reported with the reason it is in that state.

- **`display.Tracker`** — the fine answer. The ring's colour, its pattern,
  its palette, whether this is the listening ring, and when its dead-man
  switch fires.

An application that has a screen reads both: the phase for the words, the
ring for the colour and the motion.

### Every state that is not known is reported as unknown

A device that has just connected is not muted, and saying so would be a lie
the application might act on. `PhaseUnknown` and `ListenUnknown` exist for
exactly this.

### Every state carries its reason

`PhaseReason` says why the phase is what it is — "waiting for an
administrator to approve this device", "the device's mute switch is on". A
display that shows a reason is more useful than one that shows a word, and
the reason is the thing that changes when something is wrong.

## Capabilities are strings, and the SDK does not check them for you

The `protocol.Capability` type is a string, and the SDK sends what you
give it.

It could have validated the list — refusing a capability the controller
does not know, or one the SDK thinks is unimplementable. It does not,
because the SDK does not know what your device can do. Only you do. A
capability the controller does not understand is ignored there, so the cost
of announcing one by mistake is a control that does nothing — which is
exactly what the SDK would have prevented if it had checked.

What the SDK does check is the protocol's own consistency: the capability
list is the contract, and there is a test that every string in it is one
the controller understands.

## The wake word is not the SDK's business

The `wakeword` package owns the pipeline, buffering and trigger policy. It
takes an `Inferer`; the `backends/onnxruntime` module supplies an optional
Linux+cgo ONNX Runtime backend that loads local model paths. The core module
still builds without cgo. The runtime shared library and model files are
installed separately, and a client reports degraded/button-only mode if the
selected model is not available. Local listening must never silently fall
back to streaming.

### The two modes are the application's choice, and it is a privacy choice

A device that scores locally and a device that lets the controller score
both work. The difference is that the second one streams the room
continuously.

The SDK does not pick. It reports what the device is doing through
`listen_state`, and the controller shows that honestly, so the choice is
visible rather than buried in a configuration file.

## The audio format is asymmetric, and the SDK does not hide it

Capture is 16 kHz. Playback is 48 kHz.

This is worth stating plainly because it is the sort of thing that produces
audio playing at the wrong speed, and the symptom sounds like a hardware
fault rather than a format one. The SDK does not resample for you — a
device that has a resampler between its codec and its speaker needs to know
which side of it each rate belongs to, and a silent resampler in the middle
of that path is a thing worth seeing.

## The mock controller is a test double, not an emulator

It implements the parts of the protocol a device meets in a normal session:
the three planes, the registration and its three answers, the
configuration push, the ring frames, a voice turn, and the credential
issue.

It does not score wake words, talk to Home Assistant, or update firmware.
An emulator that tried would be a second implementation of the controller,
and it would be wrong in ways nobody could see.

What it does get right is the **credential path**, because that is the part
of the protocol most likely to break and the hardest to notice when it
does: the mock pushes the token and the certificate authority over the
shell plane exactly as the real controller does, heredoc and md5 and all.

It also holds a pending device the way the controller does — by closing
the connection — so a device's retry against the mock is the retry it would
make against the real thing.

## The emulator is the SDK's proof of concept

The [emulator](../clients/go/echomuse/emulator) is a device that behaves exactly as
the controller expects one to behave: it joins a real controller, gets
admitted, is issued a link credential, streams the machine's microphone
and plays the response on the machine's speaker.

It exists because the protocol is easier to trust when something speaks
it, and because an application needs something to be developed against
before the hardware exists.

### It is a package, not only a command

The command is the obvious part. The parts underneath are separable: the
audio backend is an interface, the client is the SDK's own, and the ring is
the `display` package's tracker. An application that wants a device with a
window uses those and writes its own.

### Its window is a web page

The emulator uses a web page and needs no native window toolkit. Its SDK
packages are system-neutral; the Linux command supplies an ALSA adapter and
needs cgo for audio. Local ONNX inference is a separate optional Linux+cgo
backend.

The styling follows the EchoMuse dashboard — the same warm-grey ground,
the same dark inset LCD readouts, the same colours for the device states.
An emulator that looked like a different product would be harder to trust
than one that looks like the thing it stands in for.

### It uses ALSA for audio

The emulator command supplies its Linux ALSA adapter, while the SDK exposes a system-neutral audio backend contract that applications can implement for their platform. The browser offers live device selection through that optional contract. When the backend supports clocked output, Sendspin music and voice are mixed into scheduled periods on the same output handle, preserving synchronization without competing for the speaker. Other backends can provide their own output strategy.

### The approval is not the emulator's to make

The emulator walks the real flow, including the step a person takes in the
dashboard. That is deliberate: an emulator that approved itself would
teach an application a flow the real controller does not have.

## The shell plane carries the credential, and the credential is enough

The controller reaches a device's filesystem by writing shell commands. The
wire protocol *is* a shell, so a device that answers it with a real
`/bin/sh` gets every command the controller writes — as the user running
the device.

The SDK's default runner therefore does not start a shell. It answers the
controller's credential transfer and refuses everything else: the token and
the certificate authority are written, and no other command is run. The
credential is the one thing the plane is needed for, and it is the only
thing the default gives.

A device that is a general-purpose computer can ask for the whole shell
with `AllowLocalShell`, and the SDK logs a warning when it starts.

### Why the path is mapped rather than configured on the controller

The controller names `/data/local/etc/echomuse`, and it does not ask the
device where its state lives. The device's own directory is named by the
application, in `CredentialsDir`, and the SDK maps the controller's path
onto it — for the credential-only runner, and through the `ShellRunner`'s
`PathMap` when the whole shell is exposed.

Only that one path is mapped; a command naming an unrelated file still
refers to that file.

## Reconnection is the SDK's job

`Run` reconnects with a backoff when the link drops, and the delay doubles
up to a ceiling.

An application should not have to notice a dropped link and dial again —
that is a loop every application would write slightly differently, and
every one of them would get the reconnect timing subtly wrong. The Echo
firmware has this in its `Run` loop, and so does this.

The backoff matters: a controller that is down, or a network that is, would
otherwise be hammered by every device on it.

## What V1 does not do, and why

### No bundled inference runtime or model assets

An optional ONNX Runtime backend is available, but the shared library and
models are deployment assets and must match the target architecture. The
pure-Go core remains usable without them.

### No display driver

The `display` package produces values. Drawing a colour on a framebuffer,
a GTK label or a web page is the application's business, and the libraries
are incompatible.

### No Bluetooth or firmware updates

Bluetooth proxying and firmware updates remain outside this SDK. Sendspin is
implemented as an optional player package and is capability-gated.

### No HTTP API client

The controller's HTTP API is for dashboards and automation, and it is a
different product from the device protocol. A client for it belongs in the
same repository but not in this package — the two share a name and nothing
else.

## The versioning

The SDK is versioned on its own: `emdev-v*`, independent of the Echo
firmware's `v*` and the controller's `controller-v*`.

The controller and the device negotiate by **capability**, never by
version, and the SDK follows that rule without exception. Nothing in it
compares version strings, and an application should not either: a version
comparison puts release history in the protocol and misjudges development
builds.

## What would change in V2

- A wake-word inference backend, with cgo as an opt-in build tag.
- A capture-side resampler, for devices whose codec runs at 48 kHz.
- Private-listening session routing, so an application can open a session
  and stream tagged audio without tracking the session numbers itself.
- The Bluetooth bridge, for devices that are also a Bluetooth proxy.

None of them are protocol changes. They are conveniences, and the protocol
is stable — which is the point of the two rules the controller is built on.
