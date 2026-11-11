package emulator

import (
	"time"

	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/protocol"
)

// How long the emulator waits for the controller to acknowledge a network
// change, and how often it looks.
const (
	wifiCommitTimeout = 60 * time.Second
	wifiCommitPoll    = 500 * time.Millisecond
)

// The emulator's network behaviour, in full.
//
// The controller can ask a device to scan for networks and to join one.
// The emulator does both on the machine it runs on, so the flow can be
// exercised end to end without an Echo.

// scanWifi answers the controller's scan request.
//
// It runs on its own goroutine, because a scan takes seconds and the
// control plane must not block for it. The answer carries an error rather
// than nothing when the machine cannot scan: the controller waits for this
// message and gives up after a timeout, and a silent device cannot be told
// apart from a busy one.
func (e *Emulator) scanWifi() {
	go func() {
		networks, err := e.scanner.Scan()
		if err != nil {
			e.log.Warn("the scan did not happen", "error", err)
			_ = e.device.SendWifiScanResult(protocol.WifiScanResult{
				Error: err.Error(),
			})
			return
		}
		e.log.Info("the scan finished", "networks", len(networks))
		_ = e.device.SendWifiScanResult(protocol.WifiScanResult{
			Networks: networks,
		})
	}()
}

// changeWifi answers the controller's request to join another network.
//
// The controller re-asks until it acknowledges the outcome with a commit,
// because a single answer can vanish into a half-open connection killed by
// the network switch — which is exactly what this change causes. So the
// outcome is held and re-sent until the commit arrives.
func (e *Emulator) changeWifi(change protocol.WifiChange) {
	e.mu.Lock()
	e.wifiChange = &change
	e.wifiResultSent = false
	e.mu.Unlock()

	go func() {
		err := e.scanner.Join(change.SSID, change.PSK)
		result := protocol.WifiResult{SSID: change.SSID}
		if err != nil {
			result.OK = false
			result.Error = err.Error()
			e.log.Warn("the network change failed", "ssid", change.SSID, "error", err)
		} else {
			result.OK = true
			e.log.Info("the network changed", "ssid", change.SSID)
		}
		e.sendWifiResult(result)
	}()
}

// sendWifiResult reports the outcome of a network change.
//
// It is sent again until the controller acknowledges it with a commit,
// because the answer is what the controller records and a lost answer
// leaves it waiting. The controller is explicit about this: the change
// kills the connection the answer would have travelled on, so a single
// send can vanish.
func (e *Emulator) sendWifiResult(result protocol.WifiResult) {
	e.mu.Lock()
	e.wifiResult = &result
	e.wifiResultSent = false
	e.mu.Unlock()
	e.retryWifiResult()
}

// retryWifiResult re-sends the held outcome until the controller commits.
//
// It gives up after a while. A controller that never commits is a
// controller that has gone away, and a device that retries for ever is a
// device that cannot be stopped.
func (e *Emulator) retryWifiResult() {
	e.mu.Lock()
	result := e.wifiResult
	if result == nil || e.wifiResultSent {
		e.mu.Unlock()
		return
	}
	e.wifiResultSent = true
	e.mu.Unlock()

	if err := e.device.SendWifiResult(*result); err != nil {
		e.log.Warn("the outcome did not reach the controller", "error", err)
	}

	// Wait for the commit, and send again if it does not come.
	go func() {
		deadline := time.Now().Add(wifiCommitTimeout)
		for time.Now().Before(deadline) {
			time.Sleep(wifiCommitPoll)
			e.mu.Lock()
			done := e.wifiResult == nil
			e.mu.Unlock()
			if done {
				return
			}
			e.mu.Lock()
			e.wifiResultSent = false
			e.mu.Unlock()
			e.retryWifiResult()
		}
	}()
}

// CurrentNetwork reports the network the machine is on.
func (e *Emulator) CurrentNetwork() (ssid string, signal *int, err error) {
	ssid, _, signal, err = e.scanner.Current()
	return ssid, signal, err
}

// SetWifiCommit marks the controller's acknowledgement as received.
//
// The controller records the outcome when it commits, and the device stops
// repeating it. Nothing else is owed.
func (e *Emulator) SetWifiCommit() {
	e.mu.Lock()
	e.wifiResult = nil
	e.wifiResultSent = false
	e.wifiChange = nil
	e.mu.Unlock()
}
