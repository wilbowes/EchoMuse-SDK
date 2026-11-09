// Package main is the EchoMuse sidecar.
//
// It is a device program: it owns the link to the EchoMuse controller, the
// microphone, the speaker and the wake word, and it reports what the device
// is doing to whatever is displaying it.
//
// The sidecar is a separate program rather than part of a library because
// the application that owns the display is usually not the one that should
// own the audio. A kiosk renders a web page; the page cannot hold an ALSA
// handle and mix a voice response into a Sendspin period without opening a
// competing speaker handle and losing synchronisation. So the audio stays
// here, where one process can own it.
//
// The state is written to a file and to a socket, both optional. The file is
// what a simple display reads; the socket is what a display that wants to
// be told the moment something changes reads.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse"
	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/display"
	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/protocol"
	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/state"
	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/turn"
	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/vad"
)

// State is what the sidecar reports.
//
// It is deliberately flat and small: a display wants to know what the device
// is doing, not to understand the protocol.
type State struct {
	// Connection is whether the device can reach a controller and has been
	// admitted by one. It is what a person asks about the device.
	Connection string `json:"connection"`
	// ConnectionReason says why, in words a person can read.
	ConnectionReason string `json:"connection_reason"`

	// Phase is what the device is doing right now.
	Phase string `json:"phase"`
	// PhaseReason says why.
	PhaseReason string `json:"phase_reason"`

	// Listen is what the device is doing with its microphone.
	Listen string `json:"listen"`
	// Listening is true while the microphone is open.
	Listening bool `json:"listening"`
	// Speaking is true while a response is playing.
	Speaking bool `json:"speaking"`
	// Thinking is true while the controller waits for Home Assistant.
	Thinking bool `json:"thinking"`
	// Muted is the device's own mute switch.
	Muted bool `json:"muted"`

	// Volume is the last volume level the device reported.
	Volume int `json:"volume"`

	// Level is the loudest sample the microphone has heard, as a
	// percentage of full scale. It is what a level meter draws.
	Level int `json:"level"`

	// Pattern is the ring's animation.
	Pattern string `json:"pattern"`
	// Palette is the ring's colours, in order.
	Palette []Colour `json:"palette"`

	// LastError is the most recent error, if any.
	LastError string `json:"last_error"`

	// UpdatedAt is when this state was produced.
	UpdatedAt time.Time `json:"updated_at"`
}

// Colour is one indicator's colour.
type Colour struct {
	R int `json:"r"`
	G int `json:"g"`
	B int `json:"b"`
}

// connection derives the connection state from a snapshot.
//
// The SDK reports the link and the admission separately, and a display wants
// one answer. "Connected" means the device is admitted and the link is up;
// "Unpaired" means the controller has not admitted it yet, which is the
// ordinary state of a device waiting to be approved.
func connection(s state.Snapshot) (string, string) {
	switch {
	case s.Approved && s.LinkUp:
		return "connected", "the controller admitted this device"
	case s.Approved:
		return "unpaired", "the device is admitted but the link is down"
	case s.LinkUp:
		return "unpaired", "the controller has not admitted this device yet"
	case s.Phase == state.PhaseRefused:
		return "refused", "the controller refused this device's credentials"
	default:
		return "disconnected", "the link to the controller is down"
	}
}

// Config is the device configuration the application writes.
//
// The controller owns the wake word and the audio settings, so this is what
// the device asks for rather than what it decides.
type Config struct {
	Board              string  `json:"board"`
	WakeMode          string  `json:"wakeMode"`
	WakeThreshold     float64 `json:"wakeThreshold"`
	WakeWordEnabled   bool    `json:"wakeWordEnabled"`
	WakeSound         bool    `json:"wakeSound"`
	BargeIn           bool    `json:"bargeIn"`
	StartupVolume     int     `json:"startupVolume"`
	MicGainDb         int     `json:"micGainDb"`
	VadThreshold      float64 `json:"vadThreshold"`
	VadSpeechMs       int     `json:"vadSpeechMs"`
	VadSilenceMs      int     `json:"vadSilenceMs"`
	Volume            int     `json:"volume"`
	DuckDb            float64 `json:"duckDb"`
	MuteButton        bool    `json:"muteButton"`
	VolumeButtonSound bool    `json:"volumeButtonSound"`
}

// readConfig reads the device configuration.
func readConfig(path string) (Config, error) {
	var c Config
	data, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return c, fmt.Errorf("echomuse-sidecar: decoding the configuration: %w", err)
	}
	return c, nil
}

func main() {
	var (
		controller = flag.String("controller", "", "controller URL; empty discovers by mDNS")
		deviceID   = flag.String("device-id", "home-panel", "stable device identity")
		board      = flag.String("board", "home-panel", "board name, shown in Home Assistant")
		creds      = flag.String("credentials-dir", "", "where the link credential lives")
		stateFile  = flag.String("state-file", "", "write the state here as JSON")
		configFile = flag.String("config-file", "", "read the device configuration from here")
		socketPath = flag.String("socket", "", "serve the state on this unix socket")
		verbose    = flag.Bool("verbose", false, "log at debug level")
	)
	flag.Parse()

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(log)

	if *creds == "" {
		fmt.Fprintln(os.Stderr, "echomuse-sidecar: --credentials-dir is required")
		os.Exit(2)
	}

	device, err := echomuse.New(echomuse.Options{
		DeviceID:       *deviceID,
		Version:        "0.1.0",
		Board:          *board,
		Capabilities:   capabilities(),
		ControllerURL:  *controller,
		CredentialsDir: *creds,
		Logger:         log,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "echomuse-sidecar: %v\n", err)
		os.Exit(1)
	}

	ring := display.NewTracker()
	guard := turn.New(turn.Options{
		Device:   device,
		Detector: vad.New(vad.Default()),
		Log:      log,
	})

	// The configuration is applied when the controller pushes one, and the
	// file is where the application's settings arrive.
	applyConfig := func(c protocol.Config) {
		log.Info("the controller pushed a configuration", "model", c.OwwModel, "threshold", c.OwwThreshold)
		if c.StartupVolume != nil {
			log.Info("the controller set the startup volume", "volume", *c.StartupVolume)
		}
	}
	device.OnConfig(applyConfig)
	if *configFile != "" {
		if c, err := readConfig(*configFile); err == nil {
			log.Info("read the device configuration", "file", *configFile)
			_ = c
		} else if !errors.Is(err, os.ErrNotExist) {
			log.Warn("the device configuration could not be read", "error", err)
		}
	}

	reporter := &stateReporter{
		log:    log,
		device: device,
		ring:   ring,
		guard:  guard,
	}
	if *stateFile != "" {
		reporter.file = *stateFile
	}
	if *socketPath != "" {
		reporter.socket = *socketPath
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	go reporter.run(ctx)

	go func() {
		<-ctx.Done()
		device.Stop()
	}()

	if err := device.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(os.Stderr, "echomuse-sidecar: %v\n", err)
		os.Exit(1)
	}
}

// capabilities are what this device implements.
//
// Every one of them must be true of the device: announcing a capability and
// not implementing it produces a device that scores, stays silent and looks
// broken.
func capabilities() []protocol.Capability {
	return []protocol.Capability{
		protocol.CapMic,
		protocol.CapSpeaker,
		protocol.CapPairing,
		protocol.CapButtons,
		protocol.CapOWWLocalOnly,
	}
}

// stateReporter turns the SDK's state into the small shape a display wants,
// and publishes it.
//
// The state is published when it changes rather than on a timer, so a display
// that polls the file does not see a changed file every few hundred
// milliseconds.
type stateReporter struct {
	log    *slog.Logger
	device *echomuse.Device
	ring   *display.Tracker
	guard  *turn.Guard

	// file is where the state is written, if anywhere.
	file string
	// socket is where the state is served, if anywhere.
	socket string

	mu sync.Mutex
	// last is the state that was published, so a change is not published twice.
	last State
	// haveLast is whether anything has been published yet.
	haveLast bool
}

func (r *stateReporter) run(ctx context.Context) {
	r.device.State().OnChange(func(s state.Snapshot) {
		r.publish(s)
	})
	// The ring's animation changes on its own, so it is sampled rather than
	// waited for.
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.publish(r.device.State().Snapshot())
		}
	}
}

// publish writes the state if it changed.
func (r *stateReporter) publish(s state.Snapshot) {
	conn, reason := connection(s)
	view := r.ring.View()

	next := State{
		Connection:       conn,
		ConnectionReason: reason,
		Phase:            string(s.Phase),
		PhaseReason:      s.PhaseReason,
		Listen:           string(s.Listen),
		Listening:        s.Listening,
		Speaking:         s.Speaking,
		Thinking:         s.Thinking,
		Muted:            s.Muted,
		Volume:           s.Volume,
		Pattern:          string(view.Pattern),
		LastError:        s.LastError,
		UpdatedAt:         time.Now().UTC(),
	}
	for _, c := range view.Palette {
		next.Palette = append(next.Palette, Colour{R: int(c.R), G: int(c.G), B: int(c.B)})
	}

	r.mu.Lock()
	if r.haveLast && same(r.last, next) {
		r.mu.Unlock()
		return
	}
	r.last = next
	r.haveLast = true
	r.mu.Unlock()

	if r.file != "" {
		r.writeFile(next)
	}
	if r.socket != "" {
		r.writeSocket(next)
	}
}

// same compares two states, ignoring the timestamp.
func same(a, b State) bool {
	if a.Connection != b.Connection || a.Phase != b.Phase || a.Listen != b.Listen {
		return false
	}
	if a.Listening != b.Listening || a.Speaking != b.Speaking || a.Thinking != b.Thinking || a.Muted != b.Muted {
		return false
	}
	if a.Volume != b.Volume || a.Level != b.Level || a.Pattern != b.Pattern || a.LastError != b.LastError {
		return false
	}
	if len(a.Palette) != len(b.Palette) {
		return false
	}
	for i := range a.Palette {
		if a.Palette[i] != b.Palette[i] {
			return false
		}
	}
	return true
}

// writeFile writes the state atomically, so a reader never sees half of it.
func (r *stateReporter) writeFile(s State) {
	data, err := json.Marshal(s)
	if err != nil {
		r.log.Error("the state could not be encoded", "error", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(r.file), 0o700); err != nil {
		r.log.Error("the state directory could not be created", "error", err)
		return
	}
	temporary := r.file + ".tmp"
	if err := os.WriteFile(temporary, data, 0o644); err != nil {
		r.log.Error("the state could not be written", "error", err)
		return
	}
	if err := os.Rename(temporary, r.file); err != nil {
		r.log.Error("the state could not be published", "error", err)
	}
}

// writeSocket publishes the state on the unix socket.
func (r *stateReporter) writeSocket(s State) {
	data, err := json.Marshal(s)
	if err != nil {
		return
	}
	conn, err := net.Dial("unix", r.socket)
	if err != nil {
		r.log.Debug("the state socket is not listening", "error", err)
		return
	}
	defer conn.Close()
	_, _ = conn.Write(append(data, '\n'))
}
