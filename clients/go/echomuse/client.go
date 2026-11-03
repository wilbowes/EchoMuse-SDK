// Package echomuse is a client for the EchoMuse controller.
//
// It lets a device that is not an Amazon Echo — a Raspberry Pi with a touch
// display, a speaker with a microphone, a panel on a wall — join a
// controller and be driven by the same features as an Echo: a wake word, a
// voice turn through Home Assistant, a status surface, and the controller's
// configuration.
//
// # The shape of it
//
// The SDK is a protocol client, not a framework. It opens the three
// WebSocket planes the contract defines, speaks the control and data
// protocols, and hands the application:
//
//   - a state tracker, so the application can ask what the device is doing
//     without parsing anything;
//   - callbacks for the things the controller asks of the device — play
//     this, start the microphone, stop it, show this on the ring.
//
// The application supplies the parts that are specific to its hardware: a
// microphone source, a speaker sink, and whatever it uses to show state.
//
// # The two rules
//
// Negotiate by capability. The device announces what it implements in its
// registration, and the controller gates every feature on the presence of
// the string. Nothing in this SDK compares versions.
//
// Degrade to old behaviour, never to a wrong answer. An absent measurement
// is not a zero, an unknown message is ignored, and a feature the other
// side does not have is reported as missing rather than assumed.
//
// # What this SDK does not do
//
// It does not score the wake word unless it is asked to, it does not talk to
// Home Assistant, and it does not know anything about Amazon hardware. The
// controller owns the voice pipeline; the device owns its hardware.
package echomuse

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/protocol"
	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/state"
	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/transport"
)

// ErrNotConnected is returned when an operation needs a live link.
var ErrNotConnected = errors.New("echomuse: not connected to a controller")

// ErrPendingApproval is returned when the controller has not admitted the
// device yet.
//
// It is not a failure. The controller closes the connection after it, so
// the device redials — and that retry is how it picks up the approval.
var ErrPendingApproval = errors.New("echomuse: the controller is holding this device pending approval")

// Options configures a Device.
type Options struct {
	// DeviceID identifies the device to the controller. It must be stable:
	// the controller keys the device's row, its configuration and its
	// credentials on it, and a device that changes its identity on every
	// boot is a new device every time.
	//
	// A MAC address is a reasonable choice. A random identifier stored on
	// disk is a better one, because a MAC address is readable by anything
	// on the network and the device id is public — it travels in mDNS.
	DeviceID string

	// Version is the device's firmware version. It is shown in the
	// dashboard and recorded in the device's history. It is never used to
	// decide what a device may do.
	Version string

	// Board identifies the hardware. The controller names the model in
	// Home Assistant from it, and an id it does not know falls back to the
	// Echo Dot's model.
	Board string

	// BaseOS names the userspace the device booted.
	BaseOS protocol.BaseOS

	// Capabilities are what the device implements. Every one of them must
	// be true of the device: announcing a capability and not implementing
	// it produces a device that scores, stays silent and looks broken.
	Capabilities []protocol.Capability

	// Controller addresses the controller. When ControllerURL is empty the
	// SDK finds the controller by mDNS.
	Controller transport.Controller

	// ControllerURL is the controller's base URL, for a controller that
	// mDNS cannot reach. It overrides Controller.
	ControllerURL string

	// ControllerTLSPort is the controller's encrypted-WebSocket port.
	//
	// A device that holds the controller's certificate authority dials
	// only wss, and the port is not the plain one plus one — the
	// controller advertises it over mDNS, and a device that found the
	// controller by address rather than by discovery has no other way to
	// learn it. Zero means the controller offers no encrypted port, and
	// the device dials plain.
	ControllerTLSPort int

	// Credentials are the device's link credentials. The SDK reads them
	// from disk on every dial when they are not set here, so a credential
	// the controller pushes takes effect on the next reconnect.
	Credentials transport.Credentials

	// CredentialsDir is where the credentials live on disk.
	CredentialsDir string

	// ShellRunner starts the shell sessions the controller asks for.
	//
	// The controller reaches a device's filesystem over the shell plane,
	// and it issues the device's link credential that way — so a device
	// that does not serve this plane never receives one. The default runs
	// the system's shell, which is right for a general-purpose computer.
	// A device that must not expose a general shell supplies its own.
	ShellRunner ShellRunner

	// DiscoverTimeout bounds the mDNS search.
	DiscoverTimeout time.Duration

	// ReconnectDelay is the wait before the first reconnection attempt.
	// It doubles up to ReconnectMaxDelay.
	ReconnectDelay time.Duration

	// ReconnectMaxDelay caps the backoff.
	ReconnectMaxDelay time.Duration

	// Logger receives the SDK's own diagnostics. A nil logger discards
	// them.
	Logger *slog.Logger
}

// Device is a connection to a controller.
//
// It is created with New and driven by Run, which connects, keeps the link
// up and reconnects when it drops. Every method is safe to call from any
// goroutine.
type Device struct {
	opts Options
	log  *slog.Logger

	state *state.Tracker

	mu      sync.RWMutex
	control *plane
	data    *plane
	shell   *shellSession
	link    transport.Controller
	creds   transport.Credentials

	features   map[string]bool
	capabilitY []protocol.Capability

	micSeq         uint16
	listenSession  uint32
	listenSeq      uint16
	listenAckTimer *time.Timer
	listenMaxTimer *time.Timer

	// playbackBytes counts the audio the controller has sent for the
	// current response. It is the answer to "did the reply arrive".
	playbackBytes int64

	// pairing is set while the device is asking to be admitted. It goes
	// into the registration while it is set, and the request is repeated
	// every few seconds for as long as the window is open.
	pairing   bool
	pairUntil time.Time

	callbacksMu            sync.RWMutex
	onConfig               []func(protocol.Config)
	onLEDs                 []func(protocol.LEDs)
	onLEDAnim              []func(protocol.AnimSpec)
	onMicStart             []func(lockMic bool)
	onMicStop              []func()
	onSpeaker              []func(pcm []byte)
	onSpeakerEOS           []func()
	onMusic                []func(pcm []byte)
	onMusicEOS             []func()
	onCue                  []func(cue string)
	onVolumeSet            []func(level int)
	onDuck                 []func(on bool)
	onFlush                []func(kind string)
	onListenAck            []func(session uint32)
	onListenClose          []func(protocol.ListenClose)
	onSendspinTokenRequest []func()
	onRaw                  []func(protocol.Incoming)
	onLog                  []func(level, message string)

	stopped chan struct{}
	once    sync.Once
}

// New returns a Device.
//
// It does not connect. Run does that, and keeps the connection up.
func New(opts Options) (*Device, error) {
	if opts.DeviceID == "" {
		return nil, errors.New("echomuse: a device id is required")
	}
	if len(opts.Capabilities) == 0 {
		return nil, errors.New("echomuse: at least one capability is required")
	}
	if opts.DiscoverTimeout == 0 {
		opts.DiscoverTimeout = 5 * time.Second
	}
	if opts.ReconnectDelay == 0 {
		opts.ReconnectDelay = time.Second
	}
	if opts.ReconnectMaxDelay == 0 {
		opts.ReconnectMaxDelay = 30 * time.Second
	}
	if opts.BaseOS == "" {
		opts.BaseOS = protocol.BaseOSUnknown
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}

	d := &Device{
		opts:       opts,
		log:        opts.Logger,
		state:      state.NewTracker(),
		features:   map[string]bool{},
		capabilitY: append([]protocol.Capability(nil), opts.Capabilities...),
		stopped:    make(chan struct{}),
	}
	d.state.SetCapabilities(d.capabilitY)
	return d, nil
}

// State returns the device's state tracker.
func (d *Device) State() *state.Tracker { return d.state }

// HasFeature reports whether the controller announced a feature.
//
// A controller older than 2.23.0 announces none at all, so an empty list
// means "nothing negotiated", not "nothing supported".
func (d *Device) HasFeature(name string) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.features[name]
}

// HasCapability reports whether this device announced the capability.
func (d *Device) HasCapability(cap protocol.Capability) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	for _, own := range d.capabilitY {
		if own == cap {
			return true
		}
	}
	return false
}

// Capabilities returns the capabilities this device announced.
func (d *Device) Capabilities() []protocol.Capability {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return append([]protocol.Capability(nil), d.capabilitY...)
}

// Run connects to the controller and keeps the link up.
//
// It returns when the context is cancelled or Stop is called. A connection
// that drops is re-established with a backoff, so the caller does not have
// to watch for it.
func (d *Device) Run(ctx context.Context) error {
	defer d.once.Do(func() { close(d.stopped) })

	delay := d.opts.ReconnectDelay
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		err := d.connectAndServe(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			d.log.Warn("link down", "error", err)
			d.state.SetError(err)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-d.stopped:
			return nil
		case <-time.After(delay):
		}

		// A device waiting for approval redials briskly, because the
		// approval is what unblocks it and the controller's own window
		// is minutes long. The full backoff would leave the device
		// looking dead while somebody stands at the dashboard.
		if errors.Is(err, ErrPendingApproval) {
			delay = d.opts.ReconnectDelay
		} else {
			delay *= 2
			if delay > d.opts.ReconnectMaxDelay {
				delay = d.opts.ReconnectMaxDelay
			}
		}
	}
}

// Stop closes the link and ends Run.
func (d *Device) Stop() {
	d.once.Do(func() { close(d.stopped) })
	d.closeAll()
}

// connectAndServe establishes the three planes and serves them until the
// link drops.
func (d *Device) connectAndServe(ctx context.Context) error {
	link, creds, err := d.resolveController(ctx)
	if err != nil {
		return err
	}

	d.mu.Lock()
	d.link = link
	d.creds = creds
	d.mu.Unlock()

	control, err := d.dialControl(ctx, link, creds)
	if err != nil {
		return err
	}
	d.mu.Lock()
	d.control = control
	d.mu.Unlock()

	// The controller will not admit the device until it registers, so the
	// registration goes out before anything else. The reply decides
	// whether the device is admitted at all.
	if err := d.register(ctx, control); err != nil {
		control.Close()
		return err
	}

	// A device asking to pair repeats the request while its window is
	// open, so the request stays on the dashboard.
	if d.PairingOpen() {
		go d.askToPair(ctx)
	}

	data, err := d.dialData(ctx, link, creds)
	if err != nil {
		control.Close()
		return err
	}
	d.mu.Lock()
	d.data = data
	d.mu.Unlock()

	d.state.SetLink(true)
	d.state.SetApproved(true)
	d.log.Info("connected", "controller", d.dialURL(link, creds), "device", d.opts.DeviceID)

	// Either plane dropping ends the link: the control plane carries the
	// registration and the configuration, and the data plane carries the
	// audio, so a link with only one of them is not a link.
	dropped := make(chan struct{})
	var once sync.Once
	fail := func() { once.Do(func() { close(dropped) }) }

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		defer fail()
		d.serveControl(ctx, control)
	}()
	go func() {
		defer wg.Done()
		defer fail()
		d.serveData(ctx, data)
	}()

	select {
	case <-ctx.Done():
	case <-dropped:
	}

	d.closeAll()
	wg.Wait()
	return ctx.Err()
}

// resolveController finds the controller and loads the credentials.
func (d *Device) resolveController(ctx context.Context) (transport.Controller, transport.Credentials, error) {
	link := d.opts.Controller

	if d.opts.ControllerURL != "" {
		c, err := parseControllerURL(d.opts.ControllerURL)
		if err != nil {
			return transport.Controller{}, transport.Credentials{}, err
		}
		link = c
	} else if link.Host == "" {
		discoverCtx, cancel := context.WithTimeout(ctx, d.opts.DiscoverTimeout)
		defer cancel()
		found, err := transport.Discover(discoverCtx, d.opts.DiscoverTimeout)
		if err != nil {
			return transport.Controller{}, transport.Credentials{}, err
		}
		link = found
	}

	// The encrypted port comes from the caller when the controller was
	// named by address, because mDNS is where a discovered controller
	// advertises it and there is nothing else to read it from.
	if d.opts.ControllerTLSPort > 0 {
		link.TLSPort = d.opts.ControllerTLSPort
	}

	creds := d.opts.Credentials
	if creds.Token == "" && len(creds.CAPEM) == 0 {
		loaded, err := transport.LoadCredentials(d.opts.CredentialsDir)
		if err != nil {
			d.log.Debug("no credentials on disk", "error", err)
		} else {
			creds = loaded
		}
	}

	return link, creds, nil
}
