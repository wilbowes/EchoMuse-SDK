package protocol

// This file holds the messages a device reports about its network, and the
// ones the controller asks a device to act on.
//
// The SDK does not interpret any of it. A device that has no network stack
// simply does not answer a scan, and the controller must treat a missing
// answer as unknown rather than as a failure.
//
// The shapes here follow the wire contract
// (docs/device-controller-interface.md in the controller's repository).
// Every type is a wire shape and nothing more: the behaviour belongs to the
// application, because only the application knows the hardware.

// WifiNetwork is one network a device can see.
//
// The fields are the ones a scan reports. Signal is a pointer because a
// network seen without a signal reading is not a network with a signal
// reading of zero.
type WifiNetwork struct {
	// SSID is the network name for display. An SSID that is not valid
	// UTF-8 is shown as U+FFFD here.
	SSID string `json:"ssid"`
	// SSIDHex is the network's exact bytes, as hex.
	//
	// An SSID is 0–32 arbitrary bytes, so a name alone cannot always
	// address one. This is the addressable form, and it is what the
	// controller sends back in a wifi_change for a network that came from
	// a scan.
	SSIDHex string `json:"ssid_hex,omitempty"`
	// Signal is the signal strength in dBm.
	Signal *int `json:"signal,omitempty"`
	// BSSID is the access point's hardware address.
	//
	// It is not in the wire contract's list of scan fields, so the
	// controller ignores it — but a device that has one should send it,
	// because a network is identified by its bytes and the BSSID is one
	// of them.
	BSSID string `json:"bssid,omitempty"`
}

// WifiScanResult is a device's answer to a scan request.
//
// The controller waits for this message and gives up after a timeout, so a
// device that cannot scan must answer with an error rather than stay
// silent: silence is indistinguishable from a device that is busy.
type WifiScanResult struct {
	Type string `json:"type"`
	// Networks are the visible networks.
	Networks []WifiNetwork `json:"networks,omitempty"`
	// Error says why the scan did not happen.
	Error string `json:"error,omitempty"`
}

// WifiChange is the controller asking a device to join another network.
//
// The PSK is a secret. It arrives over the control plane, so it is
// transported the same way the link credential is — and a device that logs
// its messages must not log this one.
type WifiChange struct {
	Type string `json:"type"`
	// SSID is the network to join, for display.
	SSID string `json:"ssid"`
	// SSIDHex is the network's exact bytes. Absent means the UTF-8 of
	// SSID.
	SSIDHex string `json:"ssid_hex,omitempty"`
	// PSK is the pre-shared key: empty for an open network, 8–63
	// printable ASCII characters, or a raw 64-hex PSK.
	PSK string `json:"psk,omitempty"`
}

// WifiResult is a device's answer to a network change.
//
// The device re-sends this until the controller acknowledges it with a
// wifi_commit, because a single send can vanish into a half-open
// connection killed by the network switch.
type WifiResult struct {
	Type string `json:"type"`
	// OK says whether the device joined the network.
	OK bool `json:"ok"`
	// SSID is the network it tried to join.
	SSID string `json:"ssid,omitempty"`
	// Error says why it did not join.
	Error string `json:"error,omitempty"`
}

// WifiCommit is the controller acknowledging a network change.
//
// It is the ack that lets the device stop repeating its result.
type WifiCommit struct {
	Type string `json:"type"`
}
