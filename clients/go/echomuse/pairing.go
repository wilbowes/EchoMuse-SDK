package echomuse

import (
	"context"
	"time"

	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/protocol"
	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/state"
	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/transport"
)

// Pairing is how a device asks to be admitted to a controller.
//
// The approval is the one human decision in the whole flow. An
// administrator sees the request in the dashboard and approves it, and
// that approval is what issues the device's link credential — so there is
// no separate "secure the link" step to forget.
//
// A device that has never been seen arrives as **pending**: the controller
// holds it until someone approves it. A device that asks to pair arrives
// as **pairing**, and the dashboard offers the approval directly. Both end
// the same way.
//
// # The window
//
// The window is two minutes, and the request repeats every five seconds
// for as long as it is open. The controller drops a request 30 seconds
// after its last repeat, so the repetition is what keeps the request
// alive on the dashboard.
//
// A device that is not connected asks by redialling instead, so the
// request travels with the registration.
//
// # The credential
//
// The approval is answered by the controller pushing a fresh token and the
// certificate authority over the shell plane, and the device is expected
// to reconnect. The SDK reads the credential from disk on every dial, so
// the reconnect picks it up with no restart and no extra step.
const (
	// PairWindow is how long a pairing request stays open.
	PairWindow = 2 * time.Minute
	// PairRepeat is how often the request is repeated while the window
	// is open.
	PairRepeat = 5 * time.Second
)

// StartPairing opens the pairing window.
//
// The request goes out with the next registration and repeats until the
// window closes. The device must announce the `pairing` capability for
// the controller to offer the approval.
//
// A device that already holds a link credential does not ask. It has been
// admitted, and asking again leaves the dashboard showing an approval for
// a device that is already in the fleet.
func (d *Device) StartPairing() {
	if d.Paired() {
		d.log.Debug("the device is already paired, so it did not ask")
		return
	}

	d.mu.Lock()
	d.pairing = true
	d.pairUntil = time.Now().Add(PairWindow)
	d.mu.Unlock()

	d.log.Info("pairing window opened", "seconds", int(PairWindow.Seconds()))
	d.state.SetPhase(state.PhasePending, "asking the controller to admit this device")
}

// StopPairing closes the pairing window.
func (d *Device) StopPairing() {
	d.mu.Lock()
	d.pairing = false
	d.pairUntil = time.Time{}
	d.mu.Unlock()
}

// PairingOpen reports whether the pairing window is open.
func (d *Device) PairingOpen() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.pairing && time.Now().Before(d.pairUntil)
}

// PairingRequested reports whether the device has announced the pairing
// capability.
//
// A device without it cannot ask: the controller treats it as a device it
// has not met, and the dashboard offers an approval for it rather than a
// pairing.
func (d *Device) PairingRequested() bool {
	for _, c := range d.Capabilities() {
		if c == protocol.CapPairing {
			return true
		}
	}
	return false
}

// Paired reports whether the device holds a link credential.
//
// A device holding the controller's certificate authority is paired: the
// controller refuses a wrong or missing token on the encrypted link, so a
// device that got there is a device that was admitted. A device with only a
// token is mid-transfer, which is not the same thing.
//
// The credential on disk counts, because that is where a device keeps it
// between runs. A device that has been paired once is still paired the
// next time it starts, and asking to be admitted again would leave the
// dashboard showing an approval for a device already in the fleet.
func (d *Device) Paired() bool {
	d.mu.RLock()
	held := len(d.creds.CAPEM) > 0
	dir := d.opts.CredentialsDir
	d.mu.RUnlock()

	if held {
		return true
	}
	if dir == "" {
		return false
	}
	creds, err := transport.LoadCredentials(dir)
	return err == nil && len(creds.CAPEM) > 0
}

// askToPair repeats the pairing request while the window is open.
//
// The request rides the control plane on a live link. A device that is
// not connected asks by redialling instead, so the request travels with
// the registration.
func (d *Device) askToPair(ctx context.Context) {
	ticker := time.NewTicker(PairRepeat)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !d.PairingOpen() {
				return
			}
			d.mu.RLock()
			p := d.control
			d.mu.RUnlock()
			if p == nil {
				continue
			}
			if err := p.writeJSON(map[string]string{"type": protocol.TypePairRequest}); err != nil {
				return
			}
		}
	}
}
