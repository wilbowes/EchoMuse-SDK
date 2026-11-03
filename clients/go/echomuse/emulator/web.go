package emulator

import (
	"encoding/json"
	"html/template"
	"net/http"
	"net/url"
)

// The window is a web page served by the emulator.
//
// It is a web page rather than a native window because the SDK is pure Go
// with no cgo, and a native window would mean a toolchain dependency on
// every machine that runs it. A page served on the loopback address and
// opened in the browser works everywhere, and the styling follows the
// EchoMuse dashboard so the two look like the same product.
//
// The page polls for the emulator's state. Polling rather than a WebSocket
// is deliberate: the state changes on a human timescale — somebody
// approving a device, a response starting — and a poll every 250ms is
// indistinguishable from a push while costing a fraction of the machinery.

// view is what the page is sent.
type view struct {
	// Phase is what the emulator is doing.
	Phase string `json:"phase"`
	// PhaseMsg explains it.
	PhaseMsg string `json:"phase_msg"`
	// Steps is the walkthrough's progress.
	Steps []Step `json:"steps"`
	// LEDs is the ring's current colours.
	LEDs []ledView `json:"leds"`
	// Pattern is the ring's animation.
	Pattern string `json:"pattern"`
	// Listening marks the listening ring.
	Listening bool `json:"listening"`
	// LinkUp is whether the link to the controller is up.
	LinkUp bool `json:"link_up"`
	// Approved is whether the controller admitted the device.
	Approved bool `json:"approved"`
	// HasCredential is whether the link credential has arrived.
	HasCredential bool `json:"has_credential"`
	// Audio says what the audio backend is.
	Audio string `json:"audio"`
	// MicLevel is the loudest sample the microphone has heard, as a
	// percentage of full scale.
	MicLevel int `json:"mic_level"`
	// Capturing is whether the microphone is streaming to the controller.
	Capturing bool `json:"capturing"`
	Volume    int  `json:"volume"`
	Muted     bool `json:"muted"`
	Sendspin  any  `json:"sendspin,omitempty"`
	// DeviceID is this device's identity.
	DeviceID string `json:"device_id"`
	// ControllerURL is the controller it is joining.
	ControllerURL  string        `json:"controller_url"`
	Inputs         []AudioDevice `json:"inputs"`
	Outputs        []AudioDevice `json:"outputs"`
	SelectedInput  string        `json:"selected_input"`
	SelectedOutput string        `json:"selected_output"`
}

// ledView is one indicator's colour.
type ledView struct {
	R int `json:"r"`
	G int `json:"g"`
	B int `json:"b"`
}

// server serves the window.
type server struct {
	emu     *Emulator
	mux     *http.ServeMux
	inputs  []AudioDevice
	outputs []AudioDevice
}

// newServer returns the window's server.
func newServer(emu *Emulator) *server {
	var inputs, outputs []AudioDevice
	if provider, ok := emu.audio.(AudioDeviceProvider); ok {
		inputs, outputs, _ = provider.AudioDevices()
	}
	s := &server{emu: emu, mux: http.NewServeMux(), inputs: inputs, outputs: outputs}
	s.mux.HandleFunc("/", s.handlePage)
	s.mux.HandleFunc("/api/state", s.handleState)
	s.mux.HandleFunc("/api/tap", s.handleTap)
	s.mux.HandleFunc("/api/volume", s.handleVolume)
	s.mux.HandleFunc("/api/mute", s.handleMute)
	s.mux.HandleFunc("/api/audio-devices", s.handleAudioDevices)
	return s
}

// NewHandler returns the window's HTTP handler.
//
// It is exported so a caller can serve the window itself, on its own
// address, or behind its own authentication.
func NewHandler(emu *Emulator) http.Handler {
	return newServer(emu).mux
}

// handlePage serves the window.
func (s *server) handlePage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	pageTemplate.Execute(w, nil)
}

// handleState serves the emulator's state.
func (s *server) handleState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	snap := s.emu.device.State().Snapshot()
	ring := s.emu.Ring().View()

	s.emu.mu.Lock()
	level := s.emu.level
	capturing := s.emu.capturing
	volume := s.emu.volume
	muted := s.emu.muted
	player := s.emu.sendspin
	s.emu.mu.Unlock()

	v := view{
		Phase:         s.emu.phaseValue(),
		PhaseMsg:      s.emu.phaseMessage(),
		Steps:         s.emu.Steps(),
		Pattern:       string(ring.Pattern),
		Listening:     ring.Listening,
		LinkUp:        snap.LinkUp,
		Approved:      snap.Approved,
		HasCredential: s.emu.credentialSeen(),
		Audio:         s.emu.opts.Audio.Describe(),
		MicLevel:      level,
		Capturing:     capturing,
		Volume:        volume,
		Muted:         muted,
		DeviceID:      s.emu.opts.DeviceID,
		ControllerURL: s.emu.opts.ControllerURL,
	}
	v.Inputs, v.Outputs = s.inputs, s.outputs
	if selected, ok := s.emu.audio.(AudioDeviceProvider); ok {
		v.SelectedInput, v.SelectedOutput = selected.SelectedAudioDevices()
	}
	if player != nil {
		v.Sendspin = player.Status()
	}

	for _, c := range ring.Palette {
		v.LEDs = append(v.LEDs, ledView{R: int(c.R), G: int(c.G), B: int(c.B)})
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	json.NewEncoder(w).Encode(v)
}

func (s *server) handleAudioDevices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !sameOrigin(r) {
		http.Error(w, "forbidden origin", http.StatusForbidden)
		return
	}
	var body struct {
		Input  string `json:"input"`
		Output string `json:"output"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2048)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || body.Input == "" || body.Output == "" {
		http.Error(w, "invalid audio device selection", http.StatusBadRequest)
		return
	}
	if err := s.emu.SelectAudioDevices(body.Input, body.Output); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeOK(w)
}

// handleTap reports a press of the action button.
func (s *server) handleTap(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Scheme != "http" || u.Host != r.Host {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
	}
	s.emu.TapButton()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

func (s *server) handleVolume(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !sameOrigin(r) {
		http.Error(w, "forbidden origin", http.StatusForbidden)
		return
	}
	var body struct {
		Level *int `json:"level"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || body.Level == nil || *body.Level < 0 || *body.Level > 100 {
		http.Error(w, "volume must be an integer from 0 to 100", http.StatusBadRequest)
		return
	}
	if err := s.emu.SetVolume(*body.Level); err != nil {
		http.Error(w, "device is not connected", http.StatusServiceUnavailable)
		return
	}
	writeOK(w)
}

func (s *server) handleMute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !sameOrigin(r) {
		http.Error(w, "forbidden origin", http.StatusForbidden)
		return
	}
	var body struct {
		Muted *bool `json:"muted"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || body.Muted == nil {
		http.Error(w, "invalid mute request", http.StatusBadRequest)
		return
	}
	if err := s.emu.SetMuted(*body.Muted); err != nil {
		http.Error(w, "device is not connected", http.StatusServiceUnavailable)
		return
	}
	writeOK(w)
}

func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	return err == nil && u.Scheme == "http" && u.Host == r.Host
}

func writeOK(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// phaseValue returns what the emulator is doing.
func (e *Emulator) phaseValue() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.phase
}

// phaseMessage returns why the emulator is doing it.
func (e *Emulator) phaseMessage() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.phaseMsg
}

// pageTemplate is the window.
//
// The styling follows the EchoMuse dashboard: the same warm-grey ground,
// the same dark inset LCD readouts, the same colours for the device
// states. An emulator that looked like a different product would be
// harder to trust than one that looks like the thing it stands in for.
var pageTemplate = template.Must(template.New("page").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8" />
<meta name="viewport" content="width=device-width, initial-scale=1" />
<title>EchoMuse Emulator</title>
<style>
  :root {
    --bg:        #ccc8c0;
    --surface:   #d8d4cc;
    --card:      #e0dcd4;
    --border:    #b8b4ac;
    --text:      #1e1c18;
    --text2:     #4a463f;
    --muted:     #8a8680;
    --ok:        #286040;
    --warn:      #a06020;
    --error:     #a03030;
    --accent:    #2860a0;

    --lcd-bg:    #1e2219;
    --lcd-green: #9aba80;
    --lcd-amber: #c8a141;
    --lcd-dim:   #95ae86;
    --lcd-faint: #3a4a30;

    --mono: "DM Mono", ui-monospace, "SF Mono", Menlo, Consolas, monospace;
    --sans: "DM Sans", system-ui, -apple-system, "Segoe UI", sans-serif;
  }

  * { box-sizing: border-box; }

  body {
    margin: 0;
    min-height: 100vh;
    font-family: var(--sans);
    color: var(--text);
    background: linear-gradient(160deg, var(--bg) 0%, #b8b4ac 100%);
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 24px;
  }

  .frame {
    background: linear-gradient(170deg, var(--card) 0%, var(--surface) 100%);
    border: 1px solid var(--border);
    border-radius: 20px;
    box-shadow: 0 8px 28px rgba(0,0,0,0.18), 0 1px 0 rgba(255,255,255,0.7) inset;
    width: min(860px, 100%);
    overflow: hidden;
  }

  header {
    padding: 20px 24px;
    border-bottom: 1px solid var(--border);
    display: flex;
    align-items: baseline;
    gap: 12px;
  }
  h1 { font-size: 17px; font-weight: 600; margin: 0; letter-spacing: -0.01em; }
  .sub { font-size: 12px; color: var(--muted); font-family: var(--mono); }

  .body { padding: 24px; display: grid; grid-template-columns: 260px 1fr; gap: 28px; }

  @media (max-width: 720px) {
    .body { grid-template-columns: 1fr; }
  }

  /* ── The ring ─────────────────────────────────────────────────────── */

  .ring-panel { display: flex; flex-direction: column; align-items: center; gap: 14px; }

  .ring {
    position: relative;
    width: 200px; height: 200px;
    border-radius: 50%;
    background: radial-gradient(circle at 38% 32%, #505050 0%, #2c2c2c 55%, #181818 100%);
    box-shadow: inset 0 2px 10px rgba(0,0,0,0.6);
  }

  /* The twelve indicators sit on a circle, one every 30 degrees, which
     is the ring's own geometry. A grid would read as a bar, and the
     point of drawing the ring is that it IS a ring. */
  .ring__leds { position: absolute; inset: 0; }
  .led {
    position: absolute;
    width: 9px; height: 9px; border-radius: 50%;
    background: var(--lcd-faint);
    transform: translate(-50%, -50%);
    transition: background 90ms linear, box-shadow 90ms linear;
  }
  .led--on { box-shadow: 0 0 7px currentColor; }

  .ring__core {
    position: absolute; inset: 52px;
    border-radius: 50%;
    background: radial-gradient(circle at 42% 36%, #383838 0%, #202020 100%);
    display: flex; align-items: center; justify-content: center;
  }
  .ring__state {
    font-family: var(--mono); font-size: 10px; letter-spacing: 0.12em;
    color: var(--lcd-dim); text-transform: uppercase; text-align: center;
    padding: 0 8px; line-height: 1.4;
  }

  /* ── Readouts ─────────────────────────────────────────────────────── */

  .lcd {
    background: linear-gradient(160deg, #2a2e28 0%, var(--lcd-bg) 100%);
    border-radius: 8px; padding: 12px 14px;
    font-family: var(--mono); font-size: 11px; color: var(--lcd-dim);
    border: 1px solid #1a1c18;
    width: 100%;
  }
  .lcd__row { display: flex; justify-content: space-between; gap: 12px; padding: 2px 0; }
  .lcd__row span:last-child { color: var(--lcd-green); text-align: right; }
  .lcd__row--dim span:last-child { color: var(--lcd-faint); }
  .lcd__row--amber span:last-child { color: var(--lcd-amber); }

  /* ── The walkthrough ──────────────────────────────────────────────── */

  .panel-title {
    font-size: 11px; font-weight: 600; letter-spacing: 0.1em;
    text-transform: uppercase; color: var(--muted); margin: 0 0 10px;
  }

  .steps { list-style: none; margin: 0 0 20px; padding: 0; }
  .steps li {
    display: flex; gap: 11px; align-items: flex-start;
    padding: 7px 0; font-size: 13px; color: var(--text2);
  }
  .steps .mark {
    width: 17px; height: 17px; border-radius: 50%; flex-shrink: 0; margin-top: 1px;
    border: 1.5px solid var(--border); background: var(--surface);
    display: flex; align-items: center; justify-content: center;
    font-size: 10px; color: var(--muted);
  }
  .steps .mark--done { background: var(--ok); border-color: var(--ok); color: #fff; }
  .steps .detail { display: block; font-size: 11.5px; color: var(--muted); margin-top: 1px; }

  .notice {
    background: var(--surface); border: 1px solid var(--border);
    border-left: 3px solid var(--warn);
    border-radius: 6px; padding: 11px 13px; font-size: 12.5px;
    color: var(--text2); line-height: 1.5; margin-bottom: 18px;
  }
  .notice strong { color: var(--text); font-weight: 600; }

  button {
    font-family: var(--sans); font-size: 13px; font-weight: 600;
    padding: 9px 15px; border-radius: 7px; cursor: pointer;
    border: 1px solid var(--border); background: var(--surface); color: var(--text);
    box-shadow: 0 1px 0 rgba(255,255,255,0.7) inset;
  }
  button:hover { background: var(--card); }
  button:active { transform: translateY(1px); }
  button.primary { background: var(--accent); border-color: #1e4a7a; color: #fff; }

  footer {
    padding: 14px 24px; border-top: 1px solid var(--border);
    font-family: var(--mono); font-size: 11px; color: var(--muted);
    display: flex; justify-content: space-between; gap: 12px; flex-wrap: wrap;
  }

  .meter { display: flex; align-items: center; gap: 8px; }
  .meter__track {
    width: 90px; height: 6px; border-radius: 3px;
    background: var(--border); overflow: hidden;
  }
  .meter__fill {
    display: block; height: 100%; width: 0;
    background: var(--ok); transition: width 120ms linear;
  }
  .meter--silent .meter__fill { background: var(--warn); }
</style>
</head>
<body>
  <div class="frame">
    <header>
      <h1>EchoMuse Emulator</h1>
      <span class="sub" id="device-id">—</span>
    </header>

    <div class="body">
      <div class="ring-panel">
        <div class="ring" id="ring">
          <div class="ring__leds" id="leds"></div>
          <div class="ring__core">
            <div class="ring__state" id="phase">Starting</div>
          </div>
        </div>
        <div class="lcd" id="lcd"></div>
      </div>

      <div>
        <h2 class="panel-title">Joining the controller</h2>
        <ol class="steps" id="steps"></ol>

        <div class="notice" id="notice">
          <strong>Approve this device in the dashboard.</strong>
          Open the controller's dashboard, find the device and approve it.
          The approval is what issues the link credential, and the credential
          is what puts the device on the encrypted link.
        </div>

        <button class="primary" id="tap">Press the action button</button>
        <div class="notice" style="margin-top:14px">
          <label for="mic-device">Microphone</label><br />
          <select id="mic-device"></select><br />
          <label for="speaker-device">Speaker</label><br />
          <select id="speaker-device"></select><br />
          <label for="volume">Speaker volume <span id="volume-label">50%</span></label><br />
          <input id="volume" type="range" min="0" max="100" value="50" />
          <button id="mute" type="button">Mute microphone</button>
        </div>
      </div>
    </div>

    <footer>
      <span id="audio">—</span>
      <span class="meter" id="mic">
        <span class="meter__label" id="mic-label">mic</span>
        <span class="meter__track"><span class="meter__fill" id="mic-fill"></span></span>
      </span>
      <span id="controller">—</span>
    </footer>
  </div>

<script>
const ledsEl = document.getElementById('leds');
const ringEl = document.getElementById('ring');
const phaseEl = document.getElementById('phase');
const stepsEl = document.getElementById('steps');
const lcdEl = document.getElementById('lcd');
const noticeEl = document.getElementById('notice');
const deviceIdEl = document.getElementById('device-id');
const audioEl = document.getElementById('audio');
const controllerEl = document.getElementById('controller');
const micEl = document.getElementById('mic');
const micFillEl = document.getElementById('mic-fill');
const micLabelEl = document.getElementById('mic-label');
const volumeEl = document.getElementById('volume');
const volumeLabelEl = document.getElementById('volume-label');
const muteEl = document.getElementById('mute');
const inputDeviceEl = document.getElementById('mic-device');
const outputDeviceEl = document.getElementById('speaker-device');

function updateDeviceSelect(el, devices, selected) {
  const signature = JSON.stringify([devices, selected]);
  if (el.dataset.signature === signature) return;
  el.dataset.signature = signature;
  el.replaceChildren();
  for (const d of devices || []) {
    const option = document.createElement('option'); option.value = d.name;
    option.textContent = d.description && d.description !== d.name ? d.description + ' — ' + d.name : d.name; option.title = d.name;
    option.selected = d.name === selected; el.appendChild(option);
  }
}

const LED_COUNT = 12;
const leds = [];

function buildLeds() {
  // The twelve indicators sit on a circle, one every 30 degrees, which is
  // the ring's own geometry. The first one is at the top.
  const radius = 41; // percent of the ring's radius
  for (let i = 0; i < LED_COUNT; i++) {
    const angle = (i / LED_COUNT) * Math.PI * 2 - Math.PI / 2;
    const el = document.createElement('div');
    el.className = 'led';
    el.style.left = (50 + radius * Math.cos(angle)) + '%';
    el.style.top = (50 + radius * Math.sin(angle)) + '%';
    ledsEl.appendChild(el);
    leds.push(el);
  }
}
buildLeds();

function paintLeds(colours, pattern, listening) {
  // The controller renders every animation frame itself and sends the
  // result, so the ring here is a picture of the controller's, not a
  // local animation of it. The only thing the emulator adds is the
  // listening ring's tint, because the flag means something the
  // colours do not.
  for (let i = 0; i < LED_COUNT; i++) {
    const el = leds[i];
    const c = colours[i];
    if (!c || (c.r === 0 && c.g === 0 && c.b === 0)) {
      el.className = 'led';
      el.style.background = '';
      el.style.color = '';
      continue;
    }
    const css = 'rgb(' + c.r + ',' + c.g + ',' + c.b + ')';
    el.className = 'led led--on';
    el.style.background = css;
    el.style.color = css;
  }

  ringEl.dataset.pattern = pattern || '';
  ringEl.dataset.listening = listening ? 'yes' : 'no';
}

function paintSteps(steps) {
  stepsEl.innerHTML = '';
  for (const step of steps) {
    const li = document.createElement('li');
    const mark = document.createElement('span');
    mark.className = 'mark' + (step.done ? ' mark--done' : '');
    mark.textContent = step.done ? '✓' : '';
    const body = document.createElement('div');
    body.textContent = step.name;
    if (step.detail) {
      const d = document.createElement('span');
      d.className = 'detail';
      d.textContent = step.detail;
      body.appendChild(d);
    }
    li.appendChild(mark);
    li.appendChild(body);
    stepsEl.appendChild(li);
  }
}

function paintLcd(v) {
  const rows = [
    ['Link', v.link_up ? 'up' : 'down', v.link_up ? '' : 'dim'],
    ['Admitted', v.approved ? 'yes' : 'no', v.approved ? '' : 'dim'],
    ['Credential', v.has_credential ? 'received' : 'waiting', v.has_credential ? '' : 'dim'],
    ['Ring', v.pattern || 'none', 'dim'],
    ['Sendspin', v.sendspin ? ((v.sendspin.state || 'active') + (v.sendspin.synced ? ' · synced' : '')) : 'off', v.sendspin ? '' : 'dim'],
  ];
  lcdEl.innerHTML = '';
  for (const [label, value, mod] of rows) {
    const row = document.createElement('div');
    row.className = 'lcd__row' + (mod ? ' lcd__row--' + mod : '');
    const a = document.createElement('span');
    a.textContent = label;
    const b = document.createElement('span');
    b.textContent = value;
    row.appendChild(a);
    row.appendChild(b);
    lcdEl.appendChild(row);
  }
}

function paintMic(v) {
  // The meter answers "is the microphone working". A bar that stays at
  // zero while the controller says it is listening means the microphone
  // is open and hearing nothing, which is a different fault from one
  // that never opened.
  const level = v.mic_level || 0;
  micFillEl.style.width = Math.min(100, level) + '%';
  micEl.classList.toggle('meter--silent', v.capturing && level === 0);
  micLabelEl.textContent = v.capturing ? 'mic' : 'mic idle';
}

function refresh() {  fetch('/api/state')
    .then(r => r.json())
    .then(v => {
      phaseEl.textContent = v.phase || 'Unknown';
      deviceIdEl.textContent = v.device_id;
      audioEl.textContent = 'audio: ' + v.audio;
      updateDeviceSelect(inputDeviceEl, v.inputs, v.selected_input);
      updateDeviceSelect(outputDeviceEl, v.outputs, v.selected_output);
      controllerEl.textContent = 'controller: ' + (v.controller_url || 'mDNS');
      paintMic(v);
      paintLeds(v.leds || [], v.pattern, v.listening);
      paintSteps(v.steps || []);
      paintLcd(v);
      if (document.activeElement !== volumeEl) volumeEl.value = v.volume;
      volumeLabelEl.textContent = v.volume + '%';
      muteEl.textContent = v.muted ? 'Unmute microphone' : 'Mute microphone';

      if (v.approved && v.has_credential) {
        noticeEl.innerHTML = '<strong>Joined.</strong> The device is admitted and holds a ' +
          'link credential, so it is on the encrypted link. Speak the wake word, ' +
          'or press the action button.';
      }
    })
    .catch(() => {});
}

document.getElementById('tap').addEventListener('click', () => {
  fetch('/api/tap', { method: 'POST' });
});
volumeEl.addEventListener('change', () => {
  fetch('/api/volume', { method: 'POST', headers: {'Content-Type':'application/json'}, body: JSON.stringify({level:Number(volumeEl.value)}) });
});
muteEl.addEventListener('click', () => {
  fetch('/api/state').then(r => r.json()).then(v => fetch('/api/mute', {
    method: 'POST', headers: {'Content-Type':'application/json'}, body: JSON.stringify({muted:!v.muted})
  }));
});
function selectDevices() {
  fetch('/api/audio-devices', { method:'POST', headers:{'Content-Type':'application/json'},
    body:JSON.stringify({input:inputDeviceEl.value, output:outputDeviceEl.value}) });
}
inputDeviceEl.addEventListener('change', selectDevices);
outputDeviceEl.addEventListener('change', selectDevices);

refresh();
setInterval(refresh, 250);
</script>
</body>
</html>
`))
