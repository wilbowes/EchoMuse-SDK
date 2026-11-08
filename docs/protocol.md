# The protocol

This is what the SDK speaks. It is a restatement of the EchoMuse
controller's side of the device interface — the authoritative document is
[`docs/device-controller-interface.md`](https://github.com/wilbowes/EchoMuse/blob/main/docs/device-controller-interface.md)
in the EchoMuse repository, and where this document and that one disagree,
that one is right.

The SDK does not extend the protocol. It implements it, and it implements it
exactly, because the controller ignores what it does not understand — in
silence.

---

## Three planes

The device opens three WebSocket connections to the controller and dials out
to it. The controller is found by mDNS on `_emcontroller._tcp.local`.

| Path | Payload | Direction | Purpose |
|---|---|---|---|
| `/control` | JSON text | both | Registration, configuration, status, the ring |
| `/data` | JSON, then binary | both | Microphone audio in, playback out |
| `/shell/{device_id}` | text in, binary out | device → controller | The controller's route to the device's filesystem |

The shell plane is dialled only when the controller asks for it, with a
`shell_open` message.

### The first message on the data plane

The data plane opens with an **identify**, not with audio:

```json
{"type": "identify", "device_id": "..."}
```

The controller reads the first frame to learn which device the connection
is for, and closes a data plane whose first frame is anything else —
including a microphone frame, which it has no device id to route.

### The shell plane's frame types

The controller sends shell commands as **text** frames. The Echo firmware
ignores the frame type entirely, and so does the SDK: a device that
insists on binary frames reads nothing, the shell produces no output, and
the controller reports that the device has no shell at all.

The shell's output goes back as **binary**, which is what the controller
reads it as.

### Discovery and the link

The controller advertises `_emcontroller._tcp.local` with a TXT record
carrying the optional encrypted port. The plain port is 8767 and the
encrypted one is 8770.

A device holding the controller's certificate authority dials **only** `wss`.
A device without one dials plain. A device with neither sends no credential,
and the controller admits it as pending.

The link credential travels in the `X-EM-Token` header, and it must be on
**all three** planes. The controller refuses a device that has presented its
token once and then stops presenting it, so a device that sends it on the
control plane only is admitted there and refused on the other two.

### The encrypted port, and re-pairing

A controller found by mDNS advertises its encrypted port in the TXT record.
A controller named by address does not, so the port is given separately:

```go
echomuse.Options{
    ControllerURL:     "ws://192.168.1.132:8767",
    ControllerTLSPort: 8770,
}
```

**Re-pairing needs no cable, no serial port and no adb.** The credential
travels over the **shell plane**, which is a network connection: the
controller pushes the certificate authority and the token through a shell
session, and the device reads them from disk and redials.

While a pairing window is open the device dials plain as well as encrypted.
That is what makes a re-pair possible at all: a device being paired with a
*new* controller holds that controller's certificate authority for the
first time, so the encrypted link cannot be opened until it has been
issued one. The plain fallback carries no token — the credential belongs
to the controller that issued it, and a different controller has no
business seeing it.

## Registration

The first message on the control plane, once per connection:

```json
{
  "type": "register",
  "device_id": "pi-touch-kitchen",
  "version": "0.1.0",
  "capabilities": ["mic", "speaker", "buttons", "leds", "led_anim"],
  "base_os": "unknown",
  "board": "pi-touch",
  "kernel_arch": "aarch64",
  "kernel_release": "6.1.0"
}
```

The controller answers with one of three:

| Reply | Meaning |
|---|---|
| `ack` | Admitted. Carries the controller's own feature list. |
| `pending` | Not admitted yet: unapproved, or waiting for an administrator. |
| `refused` | Not admitted: the credentials were refused. |

The `ack` carries the controller's features — `ble_adverts_data`,
`listen_session`, `output_chain`, `ble_connect`. They are read the same way
as the device's capabilities: a feature absent from the list is one the
controller cannot do.

An absent field means "not measured". A device that cannot read its kernel
version omits it rather than sending a zero.

## Capabilities

The capability list is the whole negotiation. Every feature the controller
offers to a device is gated on one of these strings.

| Capability | Commits the device to |
|---|---|
| `mic` | Streaming microphone audio |
| `speaker` | Playing the controller's audio |
| `leds` | Accepting LED frames |
| `led_anim` | Rendering an animation spec locally |
| `buttons` | Reporting button presses |
| `oww_shadow` | Scoring the wake word and reporting crossings, without acting |
| `oww_trigger` | Acting on its own wake detection |
| `button_hold` | Reporting a press duration |
| `audio_mix` | Mixing music under the voice rather than pausing |
| `oww_local_only` | Listening privately: sending nothing until the wake fires |
| `aec_hw_ref` | Taking the echo reference from a playback loopback |
| `output_chain` | Running the EQ, bass guard and limiter at the write |
| `wake_cue` | Generating its own wake confirmation |
| `volume_cue` | Previewing a volume change as a sound |
| `wake_mic` | Listening on a chosen perimeter microphone |
| `remote_volume_arc` | Showing a level arc for a remote volume change |
| `response_level` | Applying a relative gain to the voice plane |
| `stock_sound` | Voicing its own speaker |
| `ambient_light` | Having a readable light sensor |
| `sendspin` | Being a Sendspin player |
| `wake_word_off` | Honouring `wakeWordEnabled: false` |
| `pairing` | Asking to pair itself |
| `ble_connect` | Holding Bluetooth LE connections for the controller |

### Why `oww_shadow` and `oww_trigger` are two

Because firmware exists that scores and reports but has no code to act. A
controller that treated "can score" as "can trigger" would stand down its own
detector and wait for a trigger that never comes — a device that scores
perfectly and answers nothing.

A device that scores locally announces both. A device that only relays
scoring announces `oww_shadow` alone.

## The control plane

### Device → Controller

| Type | Payload | Meaning |
|---|---|---|
| `register` | see above | The handshake |
| `button` | `clickType`, `down`, `heldMs`, `muted` | A press or release |
| `mute_state` | `muted` | The mute switch changed |
| `volume_state` | `level` | The volume changed |
| `oww_wake` | `score`, `threshold`, `ageMs`, `capturedMono` | The device's wake word fired |
| `oww_shadow_cross` | `score`, `ageMs` | A crossing, reported without acting |
| `listen_state` | `state`, `reason?` | What the device is doing with its microphone |
| `listen_end` | `session`, `reason` | The device closed a session itself |
| `ambient_light` | `lux` | A light reading |
| `stats` | telemetry | The periodic report |
| `log` | `level`, `message` | A line for the dashboard |
| `pong` | `id?`, `mono?` | The keepalive answer |

### Controller → Device

| Type | Payload | Meaning |
|---|---|---|
| `ack` | `device_id`, `features[]` | Admitted, and what the controller can do |
| `refused` | — | Not admitted |
| `pending` | `pairing?` | Not admitted yet |
| `leds` | `leds[]`, `listening?` | One frame of the ring |
| `led_anim` | `anim` | An animation spec to render locally |
| `mic_start` | `lock_mic?` | Start streaming the microphone |
| `mic_stop` | — | Stop streaming |
| `listen_ack` | `session` | A private-listening wake was taken |
| `listen_close` | `session`, `reason` | End that session |
| `volume_set` | `level` | Set the volume |
| `duck` | `on` | Duck the music under a turn |
| `config` | a configuration | Push the configuration |
| `shell_open` | `pty?` | Dial the shell plane |
| `shell_close` | — | Close the shell session |
| `play_cue` | `cue` | Play a sound the device generates |
| `music_flush` / `speaker_flush` | — | Flush a buffer |
| `ping` | `id?` | The keepalive |

Unknown types are ignored, in both directions. That is the forward
compatibility rule, and it is why the SDK hands every message to
`OnRaw` as well as to the typed handlers.

## The data plane

The frame codes are **namespaced by direction**. `0x04` and `0x05` mean
different things depending on who sent them, and a device that reads them
from one global table will misread the other direction.

### Device → Controller

| Code | Name | Layout |
|---|---|---|
| `0x01` | mic | `[0x01][seq u16 BE][PCM]` |
| `0x04` | VAD end | `[0x04]` — speech was detected, and ended |
| `0x05` | no speech | `[0x05]` — no speech was ever detected |
| `0x06` | BLE adverts | `[0x06][JSON]` |
| `0x07` | session audio | `[0x07][session u32 BE][seq u16 BE][PCM]` |
| `0x08` | BLE GATT | `[0x08][JSON]` |

### Controller → Device

| Code | Name | Layout |
|---|---|---|
| `0x02` | speaker | `[0x02][PCM]` |
| `0x03` | speaker EOS | `[0x03]` |
| `0x04` | music | `[0x04][PCM]` |
| `0x05` | music EOS | `[0x05]` |
| `0x08` | BLE GATT | `[0x08][JSON]` |

The sequence number on a capture frame increments on every frame, so a gap
is visible as a gap rather than as silence. The private-listening frames
number from zero within their session.

### Audio

| Direction | Format | Rate |
|---|---|---|
| Capture | signed 16-bit little-endian, mono | 16 000 Hz |
| Playback | signed 16-bit little-endian, mono | 48 000 Hz |

The rates differ. A device that captures at one and is handed the other
plays audio at the wrong speed.

### The capture sentinels

`0x04` and `0x05` are distinct on purpose. "The wake word then silence" —
quietly giving up — has to be tellable from "spoke, and the backend had
nothing to say".

## Private listening

A device that announces `oww_local_only` can listen privately: it scores
its own wake word and sends nothing until the word fires.

When the wake fires, the device sends an `oww_wake` carrying a session
number, and the audio that follows goes up as `0x07` frames tagged with
that session. The controller acknowledges the wake with `listen_ack`, which
stops the device's acknowledgement clock, and ends the session with
`listen_close`.

The device's own limits end the session too — an acknowledgement timeout, a
maximum session length, the mute switch, a dropped link. They are enforced
on the device, so no controller failure can leave it streaming.

The controller announces `listen_session` on its ack. A device that sends
session audio to a controller that does not understand it gets nothing back
and no error, so the feature is negotiated both ways.

## The shell plane

The controller reaches a device's filesystem over the shell plane, and it is
where a device is **issued its link credential**: the token and the
certificate authority are pushed as files.

The protocol is a shell, not a command API. The controller writes command
lines and reads their output, and it pushes a file as a base64 heredoc:

```
base64 -d > /data/local/etc/echomuse/token.part << 'EMSDK_B64_EOF'
<base64>
EMSDK_B64_EOF
chmod 600 /data/local/etc/echomuse/token.part && echo TRANSFER_OK
```

Then it verifies:

```
GOT=$(md5sum /data/local/etc/echomuse/token.part | cut -d' ' -f1);
case "$GOT" in <md5>*) mv ... .part ... && echo VERIFY_OK ;;
*) rm -f ....part; echo "VERIFY_BAD:$GOT" ;; esac
```

The bytes land in a `.part` file and are renamed only once their md5
matches, so a failed transfer leaves what was at the destination untouched.

A device that does not serve this plane never receives a credential.

### The path

The controller names its own path: `/data/local/etc/echomuse`. That is the
controller's path, not the device's. A device keeps its state wherever it
likes, and the SDK maps the controller's path onto the directory the
application named in `CredentialsDir`, which is required.

## Configuration

The controller pushes the whole configuration, not a patch. A key that is
absent was not sent, and the device keeps what it had.

Several keys are **pointers** in the device's own representation, because
for those the difference between "off" and "not sent" is the difference
between a setting the user chose and a setting nobody touched:
`wakeWordEnabled`, `consolePassword`, `consoleTimeoutMin` and `duckDb`.

The keys are grouped by what they affect:

- **Microphones** — `micGainDb`, `adcDigitalGain`, `adcMicpga`,
  `vadThreshold`, `vadSpeechMs`, `vadSilenceMs`, `beamAngle`,
  `beamformingEnabled`, `agcEnabled`, `aecEnabled`, `aecDelayMs`,
  `aecTailMs`, `aecRefSource`, `wakeMic`
- **Wake word** — `owwModel`, `owwOnDevice`, `owwThreshold`,
  `wakeWordEnabled`, `bargeInEnabled`, `bargeInThreshold`, `wakeSound`,
  `wakeSoundLevel`
- **Playback** — `startupVolume`, `duckDb`, `responseLevel`,
  `volumeButtonSound`, `remoteVolumeArc`, `eqBands`, `eqLoudness`,
  `bassGuardEnabled`, `bassGuardDb`, `limiterEnabled`, `limiterThreshold`,
  `limiterRelease`
- **Ring** — `ledScene`, `ledListenColor`, `ledThinkColor`
- **Bluetooth and multi-room** — `bleProxyEnabled`,
  `bleProxyConnections`, `sendspinEnabled`, `sendspinUnpaired`,
  `sendspinName`
- **Shell console** — `consolePassword`, `consoleTimeoutMin`

A device that does not understand a key ignores it, which is the correct
degrade. The capture-chain keys in particular describe the Echo Dot's own
analog front end and mean nothing on a device with a different signal path.

## The ring

The controller renders every animation frame and sends the result, so a
device with a ring paints exactly what an Echo paints. A device that
announces `led_anim` instead receives a **spec** and renders it on its own
ticker, which keeps the ring smooth when the controller's event loop stalls
and stops a dead controller leaving the ring lit.

An animation spec:

```json
{
  "type": "led_anim",
  "anim": {
    "pattern": "spin",
    "colors": [[0, 255, 0], [0, 128, 0]],
    "periodMs": 80,
    "ttlSec": 30
  }
}
```

The patterns are `off`, `solid`, `spin`, `rotate`, `pulse` and `meter`. The
colour list means different things per pattern: the palette for `solid` and
`rotate`, `[head, trail]` for `spin`, and a modulated palette for `pulse`
and `meter`.

`ttlSec` is a dead-man switch. When it expires the ring goes dark, because
the controller that was driving it is gone.

A plain LED frame carries the indicators and a `listening` flag:

```json
{
  "type": "leds",
  "leds": [{"id": 0, "r": 0, "g": 255, "b": 0}],
  "listening": true
}
```

The `listening` flag is the one thing on the ring that means something the
colours do not: it marks the listening ring, which the beamformer's
direction overlay keys off.

## The keepalive

The controller pings every 20 seconds and closes a connection after 30 with
no pong. The pong carries the device's monotonic clock in milliseconds, any
fixed origin — the controller maps it onto its own to date the captured
audio.

Two pings travel, and they are not the same ping.

The controller sends a **WebSocket-level** ping, which gorilla answers by
itself. It also sends a **JSON** ping with a sequence id, which the device
answers with a JSON pong carrying that id, and the controller pairs the two
to measure the round trip.

The read deadline on each plane is refreshed by **every message read**, not
only by a pong. A deadline refreshed only by a WebSocket pong expires on a
perfectly healthy link, because the controller's WebSocket pings never
arrive as pongs — gorilla answers them without ever calling the pong
handler. The device sends its own WebSocket pings for the same reason: a
plane quiet in both directions has nothing to refresh its deadline with.

## What the SDK does not speak

The protocol has more in it than V1 of the SDK uses:

- **Bluetooth** — `ble_adverts`, the `0x06` frame, and the GATT bridge on
  `0x08`. A device that announces `ble_connect` gets a bridge; one that does
  not never sees a GATT frame.
- **Sendspin** — the multi-room player. The device reports its status and
  answers a token request.
- **WiFi** — the controller can ask a device to scan and to change network.
- **Firmware updates** — the controller pushes a binary and the device
  restarts into it. An Echo-specific flow with a rollback slot.

None of them are things a non-Echo device needs to be a voice satellite, and
all of them are gated on capabilities this SDK does not announce.
