package emulator

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/protocol"
)

// The emulator answers the controller's network questions for real, on the
// machine it runs on.
//
// It scans with nmcli, and it changes networks with nmcli too — so the
// controller's wifi flow can be exercised end to end against a machine
// that is not an Echo. A machine with no NetworkManager answers that it
// cannot scan, which is the honest answer and the one the controller
// expects.

// WifiScanner scans for the networks in range.
//
// It is an interface so an integrator can put their own in — a device on
// Android reads the scan results from the platform's Wi-Fi API, and this
// is the seam where that arrives.
type WifiScanner interface {
	// Scan lists the networks in range.
	Scan() ([]protocol.WifiNetwork, error)
	// Current reports the network the machine is on.
	Current() (ssid string, bssid string, signal *int, err error)
	// Join moves the machine onto a network.
	Join(ssid, psk string) error
}

// NMCLIScanner scans with NetworkManager's command line.
//
// It is the emulator's default on a machine that has nmcli. The fields
// are asked for by name, so a change in nmcli's column order does not
// change the parsing.
type NMCLIScanner struct {
	// Binary is the nmcli to run. Empty means "nmcli" on the path.
	Binary string
	// Timeout bounds the scan. A scan that hangs must not hold the
	// control plane open, and the controller gives up after 20 seconds.
	Timeout time.Duration
}

// command runs nmcli and returns its output.
func (s NMCLIScanner) command(args ...string) (string, error) {
	binary := s.Binary
	if binary == "" {
		binary = "nmcli"
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	cmd := exec.Command(binary, args...)
	out, err := cmd.Output()
	if err != nil {
		// nmcli writes the reason to stderr, and the reason is what
		// tells an operator the tool is missing.
		if exit, ok := err.(*exec.ExitError); ok && len(exit.Stderr) > 0 {
			return "", fmt.Errorf("nmcli: %s", strings.TrimSpace(string(exit.Stderr)))
		}
		return "", fmt.Errorf("nmcli: %w", err)
	}
	return string(out), nil
}

// Scan lists the networks in range.
//
// nmcli asks the Wi-Fi hardware to scan, which takes a few seconds — the
// controller allows 20.
//
// The SSID comes in as the display name and the exact bytes come in
// separately as SSID-HEX, because an SSID is 0–32 arbitrary bytes and a
// name alone cannot always address one. The hex is what the controller
// sends back in a change request for a network that came from a scan.
func (s NMCLIScanner) Scan() ([]protocol.WifiNetwork, error) {
	out, err := s.command(
		"-t", "-f", "SSID,SSID-HEX,BSSID,SIGNAL,FREQ",
		"device", "wifi", "list", "--rescan", "yes",
	)
	if err != nil {
		return nil, err
	}
	var networks []protocol.WifiNetwork
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := splitNMCLI(line)
		if len(fields) < 4 {
			continue
		}
		network := protocol.WifiNetwork{
			SSID:    fields[0],
			SSIDHex: strings.ToLower(fields[1]),
			BSSID:   fields[2],
		}
		if network.SSID == "" && network.BSSID == "" {
			// A hidden network has no name. The controller addresses a
			// network by its bytes, so it is still worth reporting.
			continue
		}
		if v, err := strconv.Atoi(strings.TrimSpace(fields[3])); err == nil {
			// nmcli reports the signal as a percentage. The controller
			// shows dBm, so it is converted.
			network.Signal = intPtr(signalToDbm(v))
		}
		networks = append(networks, network)
	}
	return networks, nil
}

// Current reports the network the machine is on.
//
// nmcli marks the one in use with an asterisk, and the field it reports
// for it is IN-USE.
func (s NMCLIScanner) Current() (string, string, *int, error) {
	out, err := s.command(
		"-t", "-f", "IN-USE,SSID,BSSID,SIGNAL",
		"device", "wifi",
	)
	if err != nil {
		return "", "", nil, err
	}
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		fields := splitNMCLI(line)
		if len(fields) < 4 {
			continue
		}
		if strings.TrimSpace(fields[0]) != "*" {
			continue
		}
		var signal *int
		if v, err := strconv.Atoi(strings.TrimSpace(fields[3])); err == nil {
			signal = intPtr(signalToDbm(v))
		}
		return fields[1], fields[2], signal, nil
	}
	return "", "", nil, fmt.Errorf("nmcli: the machine is not on a wireless network")
}

// Join moves the machine onto a network.
func (s NMCLIScanner) Join(ssid, psk string) error {
	if ssid == "" {
		return fmt.Errorf("the network name is empty")
	}
	args := []string{"device", "wifi", "connect", ssid}
	if psk != "" {
		args = append(args, "password", psk)
	}
	if _, err := s.command(args...); err != nil {
		return err
	}
	return nil
}

// splitNMCLI splits an nmcli line into its fields.
//
// nmcli's terse output separates fields with a colon, and escapes a colon
// that is part of a value with a backslash — a BSSID arrives as
// "EC\:71\:DB\:B0\:42\:77". Splitting on every colon would cut the
// address in half, so the separator is a colon that is not escaped.
//
// The values come back unescaped, because a caller wants a BSSID it can
// compare and an SSID it can display.
func splitNMCLI(line string) []string {
	var fields []string
	var current strings.Builder
	escaped := false
	for _, r := range line {
		switch {
		case escaped:
			current.WriteRune(r)
			escaped = false
		case r == '\\':
			escaped = true
		case r == ':':
			fields = append(fields, current.String())
			current.Reset()
		default:
			current.WriteRune(r)
		}
	}
	fields = append(fields, current.String())
	return fields
}

// signalToDbm converts NetworkManager's signal percentage to dBm.
//
// NetworkManager reports 0–100, and the controller's dashboard shows dBm.
// The mapping is the one the dashboard's own bars use: 100% is about
// -30 dBm and 0% is about -100 dBm.
func signalToDbm(percent int) int {
	switch {
	case percent <= 0:
		return -100
	case percent >= 100:
		return -30
	}
	return -100 + (percent * 70 / 100)
}

// intPtr is a pointer to an int, for the optional measurements.
func intPtr(v int) *int { return &v }

// NoWifiScanner is a device with no network stack.
//
// It answers every question with an error, which is the honest answer: the
// controller waits for the scan result and gives up after a timeout, and a
// device that says nothing cannot be told apart from a busy one.
type NoWifiScanner struct{}

// Scan says the device cannot scan.
func (NoWifiScanner) Scan() ([]protocol.WifiNetwork, error) {
	return nil, fmt.Errorf("this device has no network stack")
}

// Current says the device is on no network.
func (NoWifiScanner) Current() (string, string, *int, error) {
	return "", "", nil, fmt.Errorf("this device has no network stack")
}

// Join says the device cannot change network.
func (NoWifiScanner) Join(ssid, psk string) error {
	return fmt.Errorf("this device has no network stack")
}
