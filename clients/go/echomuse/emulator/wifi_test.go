package emulator

import "testing"

// TestSplitNMCLI checks the parsing of nmcli's terse output.
//
// nmcli separates its fields with a colon and escapes a colon that is part
// of a value. Splitting on every colon cuts a BSSID in half, and a BSSID
// cut in half names no access point at all.
func TestSplitNMCLI(t *testing.T) {
	tests := []struct {
		name string
		line string
		want []string
	}{
		{
			// nmcli escapes every colon in a BSSID, so an unescaped
			// one does not arrive — but the parser does not care, and
			// a row with a plain colon in the name is a row it must
			// survive.
			name: "a plain row",
			line: "HouseOfBygrove:486f757365:DA:88:1B:63:43:8C:100:5300 MHz",
			want: []string{"HouseOfBygrove", "486f757365", "DA", "88", "1B", "63", "43", "8C", "100", "5300 MHz"},
		},
		{
			name: "an escaped bssid",
			line: `BTHub:4254487562:EC\:71\:DB\:B0\:42\:77:100:2437 MHz`,
			want: []string{"BTHub", "4254487562", "EC:71:DB:B0:42:77", "100", "2437 MHz"},
		},
		{
			name: "a hidden network with no name",
			line: `::EC\:71\:DB\:B0\:42\:7B:64:2462 MHz`,
			want: []string{"", "", "EC:71:DB:B0:42:7B", "64", "2462 MHz"},
		},
		{
			name: "a name with a colon in it",
			line: `my\:network:6d79206e6574776f726b:AA\:BB\:CC\:DD\:EE\:FF:80:2412 MHz`,
			want: []string{"my:network", "6d79206e6574776f726b", "AA:BB:CC:DD:EE:FF", "80", "2412 MHz"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitNMCLI(tt.line)
			if len(got) != len(tt.want) {
				t.Fatalf("the row split into %d fields %q, want %d %q",
					len(got), got, len(tt.want), tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("field %d was %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// TestSignalToDbm checks the conversion of nmcli's signal percentage.
//
// The controller's dashboard shows dBm, and nmcli reports a percentage.
func TestSignalToDbm(t *testing.T) {
	tests := []struct {
		percent int
		want    int
	}{
		{percent: 0, want: -100},
		{percent: 100, want: -30},
		{percent: 50, want: -65},
		{percent: 80, want: -44},
	}
	for _, tt := range tests {
		if got := signalToDbm(tt.percent); got != tt.want {
			t.Errorf("%d%% became %d dBm, want %d", tt.percent, got, tt.want)
		}
	}
}

// TestNoWifiScannerAnswersWithAnError checks that a device with no radio
// says so.
//
// The controller waits for the scan result and gives up after a timeout. A
// silent device cannot be told apart from a busy one, and the operator is
// told a scan timed out rather than that the device has no radio.
func TestNoWifiScannerAnswersWithAnError(t *testing.T) {
	var s WifiScanner = NoWifiScanner{}

	if _, err := s.Scan(); err == nil {
		t.Error("the scan did not fail, want an error")
	}
	if _, _, _, err := s.Current(); err == nil {
		t.Error("the current network did not fail, want an error")
	}
	if err := s.Join("somenet", "secret"); err == nil {
		t.Error("the join did not fail, want an error")
	}
}
