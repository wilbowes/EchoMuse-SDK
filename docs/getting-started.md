# Getting a device connected

This walks from a controller on your network to a device that answers the
wake word. It is written for someone building a device that is not an Echo:
a Raspberry Pi with a touch display, a speaker with a microphone, a panel on
a wall.

The reference implementation is the [emulator](../clients/go/echomuse/emulator), a
device that behaves exactly as the controller expects one to behave, and
every snippet here appears in it.

---

## Before you start

You need:

- an **EchoMuse controller** running on your network;
- a **device** — anything that can run Go and has a network connection;
- the **Go toolchain**, 1.24 or later.

You do not need an Echo, and you do not need to root anything.

## 1. Decide what your device can do

The controller gates every feature on the capabilities your device
announces. Announcing one you cannot honour produces a control on the
dashboard that does nothing, and a device that scores but stays silent
looks broken.

The capability list, and what each one commits you to:

| Capability | Your device must |
|---|---|
| `mic` | Stream microphone audio to the controller. |
| `speaker` | Play the controller's audio. |
| `leds` | Accept LED frames — paint them on a ring, or map them to a screen. |
| `led_anim` | Render an animation spec locally, on your own ticker. |
| `buttons` | Report button presses. |
| `button_hold` | Report how long a press lasted. |
| `audio_mix` | Mix music under the voice rather than pausing it. |
| `oww_shadow` | Score the wake word and report crossings without acting on them. |
| `oww_trigger` | Score the wake word and act on it — report it as a wake. |
| `oww_local_only` | Keep audio on the device until the wake word fires. |
| `output_chain` | Run the EQ, bass guard and limiter yourself, at the write. |
| `ambient_light` | Have a light sensor you can read. |

A device that only streams audio and plays it back needs two:

```go
Capabilities: []protocol.Capability{protocol.CapMic, protocol.CapSpeaker},
```

If your device has a screen and you want it to show the controller's status,
add `leds` and `led_anim`. The controller will then send the ring frames,
and the `display` package turns them into something you can draw.

## 2. Give the device an identity

The device id must be **stable**. The controller keys the device's row, its
configuration and its credentials on it, so a device that changes its
identity on every boot is a new device every time — and every approval and
every setting is lost.

```go
device, err := echomuse.New(echomuse.Options{
	DeviceID:       "pi-touch-kitchen",
	Version:        "0.1.0",
	Board:          "pi-touch",
	Capabilities:   capabilities,
	CredentialsDir: "/var/lib/echomuse",
})
```

`CredentialsDir` is where the device keeps its own state, and it is
required — see [keep the link credential](#11-keep-the-link-credential).
A hostname works on a machine with a fixed one. A random identifier written
to disk on first boot works better, and the reason is that the device id is
public: it travels in mDNS, so anything on the network can read it.

The `Board` field names the hardware. The controller shows the model in Home
Assistant from it, and an id it does not know falls back to the Echo Dot's
model. That is worth knowing: until the controller learns a board id, your
device appears in Home Assistant as an Echo Dot.

## 3. Find the controller

Leave `ControllerURL` empty and the SDK finds the controller by mDNS, on
the service `_emcontroller._tcp.local`. That works on the same network, and
it is what the Echo firmware does.

```go
// Found by mDNS.
device, err := echomuse.New(echomuse.Options{ /* ... */ })
```

Set it when the controller is on another subnet, or in a test:

```go
device, err := echomuse.New(echomuse.Options{
	ControllerURL: "ws://192.168.1.10:8767",
})
```

The controller listens on port 8767 for the plain link and 8770 for the
encrypted one. The SDK dials the encrypted link when it holds the
controller's certificate authority, and the plain one otherwise.

A controller found by mDNS advertises its encrypted port in the TXT
record, so nothing has to be given. One named by address does not, so the
port is named separately:

```go
device, err := echomuse.New(echomuse.Options{
	ControllerURL:     "ws://192.168.1.10:8767",
	ControllerTLSPort: 8770,
})
```

Without it, a device holding a certificate authority has no encrypted port
to dial and falls back to plain — which the controller refuses, because the
credential belongs to the encrypted link.

## 4. Bring the device up

```go
log.Fatal(device.Run(context.Background()))
```

`Run` connects, keeps the link up, and reconnects with a backoff when the
link drops. It returns when the context is cancelled.

The device registers itself on connect, and the controller either admits it
or holds it pending approval. **A new device is pending until an
administrator approves it** in the dashboard. That is the controller's
default and it is deliberate: a device on your network that has never been
vouched for should not join the fleet by itself.

The device's state tells you where it got to:

```go
device.State().OnChange(func(s state.Snapshot) {
	switch s.Phase {
	case state.PhasePending:
		log.Println("waiting for approval")
	case state.PhaseReady:
		log.Println("approved")
	}
})
```

### Asking to be admitted

A device that has never been seen and a device that **asks** are treated
differently, and the difference is worth having.

A device that does not ask arrives as **pending**: the dashboard shows it
as an ordinary device waiting for approval.

A device that asks arrives as **pairing**: the dashboard offers the
approval directly. To ask, announce the `pairing` capability and open the
window:

```go
device.StartPairing()
```

The request goes out with the next registration and repeats every five
seconds for as long as the window is open — which is what keeps it on the
dashboard, because the controller drops a request 30 seconds after its
last repeat. The window is two minutes.

Open it before the first dial, so the very first registration already
carries the request. A device that asks after it has been seen as pending
still gets admitted, but the request arrives later and the administrator
sees an ordinary pending device rather than a pairing one.

```go
device.StartPairing()
log.Fatal(device.Run(context.Background()))
```

The approval is the one human decision in the flow, and it is also what
issues the link credential — so there is no separate "secure the link"
step to forget. See [the link credential](#11-keep-the-link-credential).

## 5. Answer the microphone

The controller asks for the microphone; the device does not start on its
own. That is the right order — a device streaming before it is asked is a
device streaming to nobody.

```go
device.OnMicStart(func(lockMic bool) {
	go func() {
		for {
			pcm := readMicrophone()
			if err := device.SendMicAudio(pcm); err != nil {
				return
			}
		}
	}()
})

device.OnMicStop(func() {
	stopMicrophone()
})
```

### The audio format

The two sides use **different rates**, and getting this wrong produces
audio that plays at the wrong speed.

| Direction | Format | Rate |
|---|---|---|
| Device → controller (capture) | signed 16-bit little-endian, mono | **16 000 Hz** |
| Controller → device (playback) | signed 16-bit little-endian, mono | **48 000 Hz** |

The `lockMic` argument tells you which kind of stream it is. Without it the
stream is the always-on wake stream: ungated, continuous, AGC-free, so the
wake scorer sees an uninterrupted signal. With it the stream is a bounded
turn, and the device ends it with a sentinel.

### Ending a bounded turn

The two sentinels are not interchangeable, and the controller needs the
difference:

```go
// Speech was detected, and then ended.
device.SendVADEnd()

// No speech was ever detected before the timeout.
device.SendNoSpeechTimeout()
```

"the wake word then silence" has to be tellable from "spoke, and the
backend had nothing to say".

### The device ends a bounded turn itself

A bounded turn is the one case where the microphone opens without a wake
word — the user pressed a button, or Home Assistant asked a follow-up
question. The limits on it are the **device's** own, because the controller
may fail in the middle of one:

- the turn starts when there is speech in the audio, so the room is not
  sent before anybody has answered;
- the turn ends after five seconds of nothing after the last speech;
- the turn ends if it has run for as long as a turn may run.

A device that waits for `mic_stop` and gets none streams the room for ever.
The `vad` package implements the first two rules and is pure Go:

```go
detector := vad.New(vad.Default())
detector.Open(time.Now())

// On every capture frame:
switch detector.Push(pcm, time.Now()) {
case vad.Speaking:
	device.SendMicAudio(pcm)
case vad.Quiet:
	device.SendVADEnd()
	// Stop the capture: the turn is over.
case vad.NoSpeech:
	device.SendNoSpeechTimeout()
	// Stop the capture: nobody answered.
case vad.TimedOut:
	device.SendVADEnd()
	// Stop the capture: the turn ran for as long as it may.
}
```

The controller still ends the turn at the end of speech, exactly as it
closes a session. These limits are what the device enforces when the
controller does not.

## 6. Play the response

```go
device.OnSpeaker(func(pcm []byte) {
	playToSpeaker(pcm)
})

device.OnSpeakerEOS(func() {
	finishPlayback()
})
```

The controller sends the response in pieces and ends it with an end-of-stream
frame. A barge-in or a cancellation flushes what is buffered:

```go
device.OnFlush(func(kind string) {
	// "speaker" or "music"
	flushPlayback()
})
```

## 7. Show the state

The `state` package is what the SDK is for. It turns the protocol into
values, so your application never has to interpret a message.

```go
device.State().OnChange(func(s state.Snapshot) {
	switch s.Phase {
	case state.PhaseListening:
		showListening()
	case state.PhaseThinking:
		showThinking()
	case state.PhaseSpeaking:
		showSpeaking()
	case state.PhaseMuted:
		showMuted()
	case state.PhasePending:
		showPending()
	}
})
```

The phase is a coarse answer. The fine-grained answer — what the
controller's ring is doing right now — comes from the `display` package.

## 8. Draw the ring on a screen

The controller's status surface is a ring of twelve indicators, and it
renders every frame itself. Your device gets the finished frames, so a
device with a ring paints exactly what an Echo paints.

A device with a screen wants something else. The `display` package turns the
frames into values:

```go
ring := display.NewTracker()
device.OnLEDs(ring.OnLEDs)
device.OnLEDAnim(ring.OnLEDAnim)

device.State().OnChange(func(s state.Snapshot) {
	if !s.LinkUp {
		ring.Reset()
	}
})

// Then, whenever you redraw:
view := ring.View()
switch view.Pattern {
case display.PatternSolid:
	drawSolid(view.Colour)
case display.PatternSpin:
	drawSpinner(view.Colour, view.Secondary)
case display.PatternMeter:
	drawMeter(view.Colour)
}
```

`view.Listening` marks the listening ring — the one thing on the ring that
means something the colours do not.

`view.ExpiresAt` is the dead-man switch. When it passes, the controller is
gone, and the display must stop showing an animation that no longer means
anything.

**Clear the ring when the link drops.** A frozen display says something is
happening, and nothing is.

## 9. Treat a tap as the button

A tap on the display starts a voice turn. The duration is measured on the
device, because timing it on the controller would put the gesture at the
mercy of the round trip — and a hold misread as a tap is a gesture the user
did not make.

```go
button := NewButton(device, log)

button.Press()    // on touch down
button.Release()  // on touch up
```

The controller recognises one button code. The SDK sends it for you.

A press reports the device's mute state alongside it, because the controller
decides what a press means partly from whether the device is muted, and the
event's own value is authoritative: it carries the state at the instant of
the press.

## 10. Say what the device is doing with its microphone

This is the message a privacy statement rests on, so it is reported from
what the device actually does rather than from what it was configured to do.

```go
// The device scores its own wake word and sends nothing until it fires.
device.SendListenState(protocol.ListenLocal, "")

// The device streams continuously to the controller.
device.SendListenState(protocol.ListenStream, "streaming to the controller")

// The device cannot run its own wake word. It is button-only.
device.SendListenState(protocol.ListenDegraded, "no wake word model")
```

The controller shows this on the dashboard and summarises the fleet, and it
is honest about a device that streams.

## 11. Keep the link credential

The controller issues every device a **link token** — the secret that
identifies it on the encrypted link — by pushing it over the shell plane
after an administrator approves it. The device re-reads it on every dial, so
a rotated credential takes effect on the next reconnect without a restart.

The SDK serves the shell plane for you. **A device that does not serve it
never receives a credential**, and stays on the plain link forever.

By default the plane carries the controller's credential transfer and
nothing else — the controller can push the token and the certificate
authority, and it cannot run a command on your device. That is deliberate:
a shell on the device is a shell on the machine the device runs on.

Two things to know about it:

- **The controller names its own path**: `/data/local/etc/echomuse`. That is
  the controller's path, not yours. The SDK maps it onto the directory you
  name in `CredentialsDir`:

  ```go
  device, err := echomuse.New(echomuse.Options{
      CredentialsDir: "/var/lib/echomuse",
  })
  ```

  `CredentialsDir` is required. A device that does not say where its state
  lives cannot be given a credential, and the SDK asks rather than guessing.

- **A device that must not expose a general shell** implements `ShellRunner`
  itself and answers only what it chooses:

  ```go
  device, err := echomuse.New(echomuse.Options{
      CredentialsDir: "/var/lib/echomuse",
      ShellRunner:    myRestrictedShell{},
  })
  ```

- **A device that is a general-purpose computer** can have the host's shell
  instead, and the SDK will warn you when it starts:

  ```go
  device, err := echomuse.New(echomuse.Options{
      CredentialsDir:  "/var/lib/echomuse",
      AllowLocalShell: true,
  })
  ```

## 12. Report what you are doing

Log lines are persisted by the controller and shown on the dashboard, so
they are how a device says what it is doing to the person administering it.

```go
device.SendLog("info", "the display is on")
```

The controller also keeps a periodic telemetry record. Every field is
optional, and an absent one means "not measured" — a device that reports
zero for something it cannot measure reads as having measured zero, which
is a different and wrong answer.

```go
device.SendStats(protocol.Stats{
	CPUPct:     12.5,
	MemUsedMb:  180,
	MemTotalMb: 1024,
})
```

## Where the wake word runs

This is the one decision with a privacy consequence, so it is worth being
explicit about.

**The controller scores it** — the default. Your device streams its
microphone continuously and the controller decides when a turn starts. It is
the simplest thing to build, and it means the audio of your room is on your
network all the time. The dashboard labels such a device honestly.

**Your device scores it** — the `oww_local_only` way. The device runs the
wake-word model itself and sends nothing until the word has been said. Only
the speech that follows the wake word leaves the device.

The SDK's `wakeword` package holds the pipeline for the second way. It is
the openWakeWord pipeline in three chained models — a melspectrogram, an
embedding and a classifier — with pure-Go buffering and a local ONNX Runtime
backend available in the `backends/onnxruntime` module:

```go
detector, err := wakeword.New(myInferer)
trigger := wakeword.NewTrigger(0.5, func(c wakeword.Crossing) bool {
	device.SendOWWWake(protocol.OWWWake{
		Score:     c.Score,
		Threshold: c.Threshold,
	})
	return true
})

// Then, for each 80ms of captured audio:
score, err := detector.Push(pcm)
if err == nil {
	trigger.Consider(score, time.Now())
}
```

You can use the supplied ONNX Runtime backend or provide another local
`Inferer`. The SDK does not bundle the shared runtime or model files; install
architecture-matched assets and keep inference local in private-listening mode.

The controller selects the wake-word model in configuration. For this SDK,
install the corresponding classifier and shared feature models locally, then
check that the loaded classifier matches the configured model before reporting
private listening:

```go
device.OnConfig(func(c protocol.Config) {
	if c.OwwModel != "" {
		loadModel(c.OwwModel)
	}
})
```

## Testing against the mock

The SDK ships a mock controller, so an application can be developed and
tested without one running.

```go
mock := mockcontroller.New(mockcontroller.Options{})
defer mock.Close()

device, err := echomuse.New(echomuse.Options{
	ControllerURL:  mock.URL(),
	CredentialsDir: t.TempDir(),
})
```

The mock accepts the three planes, answers a registration with an ack (or a
pending, or a refused, when you ask for one), pushes a configuration and the
ring frames, drives a voice turn, and issues a link credential over the
shell plane exactly as the real controller does — so the credential path is
exercised, not just the happy path.

```go
mock.PushConfig(protocol.Config{OwwThreshold: 0.5})
mock.SendLEDs([]protocol.LED{{ID: 0, R: 0, G: 255, B: 0}}, true)
mock.RequestMic(true)
mock.SendPlayback(pcm)
mock.IssueCredential("token", caPEM)
```

## Checklist

- [ ] The device id is stable across reboots.
- [ ] Every announced capability is true of the device.
- [ ] The state directory is named, writable, and not shared with another
      device.
- [ ] The device asks to be admitted, if it wants the pairing approval
      rather than the ordinary one.
- [ ] Capture is 16 kHz signed 16-bit mono; playback is 48 kHz.
- [ ] The microphone starts when the controller asks, not before.
- [ ] A bounded turn ends with the right sentinel.
- [ ] A bounded turn ends by itself: the device's own limits end it when
      the controller does not.
- [ ] The display clears when the link drops.
- [ ] The listen state says what the device actually does.
- [ ] The shell plane is served, or the credential path is mapped.
- [ ] The controller's credential path is mapped to wherever the device
      keeps its state.
