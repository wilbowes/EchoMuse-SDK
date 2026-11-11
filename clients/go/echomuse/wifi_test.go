package echomuse_test

import (
	"testing"
	"time"

	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/mockcontroller"
	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/protocol"
)

// The wifi tests cover the exchange the controller drives: it asks the
// device to scan, and it asks the device to join another network.
//
// The controller waits for the scan result and gives up after a timeout,
// so a device that says nothing cannot be told apart from a busy one. That
// is why the result carries an error rather than being silent.

// TestTheScanIsAnswered checks that a device answers the controller's
// scan request.
func TestTheScanIsAnswered(t *testing.T) {
	td := newTestDevice(t, mockcontroller.ModeApprove)
	waitRegistered(t, td)

	signal := -42
	if err := td.device.SendWifiScanResult(protocol.WifiScanResult{
		Networks: []protocol.WifiNetwork{
			{SSID: "testnet", SSIDHex: "746573746e6574", Signal: &signal},
		},
	}); err != nil {
		t.Fatalf("answering the scan: %v", err)
	}

	waitFor(t, "the scan result", func() bool {
		return len(td.mock.WifiScans()) > 0
	})

	scans := td.mock.WifiScans()
	if len(scans[0].Networks) != 1 {
		t.Fatalf("the answer carried %d networks, want 1", len(scans[0].Networks))
	}
	if scans[0].Networks[0].SSID != "testnet" {
		t.Errorf("the network was %q, want testnet", scans[0].Networks[0].SSID)
	}
	if scans[0].Networks[0].Signal == nil || *scans[0].Networks[0].Signal != -42 {
		t.Errorf("the signal was %v, want -42", scans[0].Networks[0].Signal)
	}
}

// TestAFailedScanSaysSo checks that a device that cannot scan answers
// with an error rather than staying silent.
//
// The controller waits for the answer and gives up after a timeout. A
// silent device is indistinguishable from a busy one, and the operator is
// told a scan timed out rather than that the device has no radio.
func TestAFailedScanSaysSo(t *testing.T) {
	td := newTestDevice(t, mockcontroller.ModeApprove)
	waitRegistered(t, td)

	if err := td.device.SendWifiScanResult(protocol.WifiScanResult{
		Error: "this device has no network stack",
	}); err != nil {
		t.Fatalf("answering the scan: %v", err)
	}

	waitFor(t, "the scan result", func() bool {
		return len(td.mock.WifiScans()) > 0
	})

	scans := td.mock.WifiScans()
	if scans[0].Error == "" {
		t.Error("the answer carried no error, want the reason the scan failed")
	}
	if len(scans[0].Networks) != 0 {
		t.Errorf("the answer carried %d networks, want none", len(scans[0].Networks))
	}
}

// TestTheNetworkChangeIsAnswered checks that a device answers the
// controller's request to join another network.
func TestTheNetworkChangeIsAnswered(t *testing.T) {
	td := newTestDevice(t, mockcontroller.ModeApprove)
	waitRegistered(t, td)

	if err := td.device.SendWifiResult(protocol.WifiResult{
		OK:   true,
		SSID: "newnet",
	}); err != nil {
		t.Fatalf("answering the change: %v", err)
	}

	waitFor(t, "the result", func() bool {
		return len(td.mock.WifiResults()) > 0
	})

	results := td.mock.WifiResults()
	if !results[0].OK {
		t.Error("the result was a failure, want a success")
	}
	if results[0].SSID != "newnet" {
		t.Errorf("the network was %q, want newnet", results[0].SSID)
	}
}

// TestAFailedChangeIsReported checks that a device that could not join the
// network says so.
//
// The controller re-asks until it acknowledges the answer, and it records
// the failure. A device that fails silently leaves the controller waiting
// for an answer that never comes.
func TestAFailedChangeIsReported(t *testing.T) {
	td := newTestDevice(t, mockcontroller.ModeApprove)
	waitRegistered(t, td)

	if err := td.device.SendWifiResult(protocol.WifiResult{
		OK:    false,
		SSID:  "newnet",
		Error: "the passphrase was refused",
	}); err != nil {
		t.Fatalf("answering the change: %v", err)
	}

	waitFor(t, "the result", func() bool {
		return len(td.mock.WifiResults()) > 0
	})

	results := td.mock.WifiResults()
	if results[0].OK {
		t.Error("the result was a success, want a failure")
	}
	if results[0].Error == "" {
		t.Error("the result carried no error, want the reason it failed")
	}
}

// waitRegistered waits for the device to be registered with the controller.
//
// A device that has not registered has no control plane, and everything it
// sends goes nowhere.
func waitRegistered(t *testing.T, td *testDevice) {
	t.Helper()
	select {
	case <-td.mock.Registered():
	case <-time.After(10 * time.Second):
		t.Fatal("the device was never registered")
	}
}
