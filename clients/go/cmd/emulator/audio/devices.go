package audio

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/emulator"
)

const (
	DeviceDefault = "default"
	FormatALSA    = "alsa"
)

type Device struct {
	Name        string
	Format      string
	Target      string
	Description string
	Default     bool
}

func (d Device) String() string { return d.Name }
func ParseDevice(raw string) Device {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultDevice()
	}
	raw = strings.TrimPrefix(raw, FormatALSA+":")
	return Device{Name: FormatALSA + ":" + raw, Format: FormatALSA, Target: raw}
}
func defaultDevice() Device {
	return Device{Name: DeviceDefault, Format: FormatALSA, Target: DeviceDefault, Description: "System default", Default: true}
}
func defaultFormat() string { return FormatALSA }
func ListDevices() (inputs, outputs []emulator.AudioDevice, err error) {
	if runtime.GOOS != "linux" {
		return nil, nil, fmt.Errorf("direct ALSA audio is supported on Linux only")
	}
	inputs = append(inputs, emulator.AudioDevice{Name: DeviceDefault, Description: "System default", Default: true})
	outputs = append(outputs, emulator.AudioDevice{Name: DeviceDefault, Description: "System default", Default: true})
	in, out, e := listALSADevices()
	if e == nil {
		for _, d := range in {
			inputs = append(inputs, emulator.AudioDevice{Name: d.Name, Description: d.Description, Default: d.Default})
		}
		for _, d := range out {
			outputs = append(outputs, emulator.AudioDevice{Name: d.Name, Description: d.Description, Default: d.Default})
		}
	}
	sort.Slice(inputs, func(i, j int) bool { return inputs[i].Name < inputs[j].Name })
	sort.Slice(outputs, func(i, j int) bool { return outputs[i].Name < outputs[j].Name })
	return inputs, outputs, nil
}
func sortDevices(devices []Device) {
	sort.Slice(devices, func(i, j int) bool { return devices[i].Name < devices[j].Name })
}
func fileExists(path string) bool { _, err := os.Stat(path); return err == nil }
func atoi(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return n
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// alsaCardRE matches a card line in /proc/asound/cards.
var alsaCardRE = regexp.MustCompile(`^\s*(\d+)\s+\[([^\]]+)\]:\s*(.*)$`)

// alsaCard is one card, as the kernel names it.
type alsaCard struct {
	// ID is the card's short name, which is what `hw:CARD=` takes.
	ID string
	// Description is what the card is.
	Description string
	// LongName is the hardware's own name, when the kernel gives one.
	LongName string
}

// listALSADevices reads the ALSA cards from the kernel.
//
// The card list gives the cards and what they are. The device list is
// read from alsa-utils when it is installed, because it names each device
// the way the mixer does; the kernel's own list is the fallback.
func listALSADevices() (inputs []Device, outputs []Device, err error) {
	cards, err := readALSACards()
	if err != nil {
		return nil, nil, err
	}

	if in, out, ok := listALSAUtilsDevices(cards); ok {
		return in, out, nil
	}

	for _, d := range listProcPCMs(cards) {
		if d.Capture {
			inputs = append(inputs, d.Device)
		}
		if d.Playback {
			outputs = append(outputs, d.Device)
		}
	}
	return inputs, outputs, nil
}

// readALSACards reads the cards from the kernel.
func readALSACards() (map[int]alsaCard, error) {
	data, err := os.ReadFile("/proc/asound/cards")
	if err != nil {
		return nil, err
	}

	cards := map[int]alsaCard{}
	current := -1

	for _, line := range strings.Split(string(data), "\n") {
		m := alsaCardRE.FindStringSubmatch(line)
		if m != nil {
			number := atoi(m[1])
			description := strings.TrimSpace(m[3])
			_, name, found := strings.Cut(description, " - ")
			if !found {
				name = description
			}
			cards[number] = alsaCard{ID: strings.TrimSpace(m[2]), Description: strings.TrimSpace(name)}
			current = number
			continue
		}
		// The hardware's own name is on the continuation line.
		if current >= 0 {
			trimmed := strings.TrimSpace(line)
			if trimmed != "" {
				card := cards[current]
				if card.LongName == "" {
					card.LongName = trimmed
					cards[current] = card
				}
			}
		}
	}

	for number, card := range cards {
		if card.LongName != "" &&
			(card.Description == "" || card.Description == card.ID) {
			card.Description = card.LongName
			cards[number] = card
		}
	}
	return cards, nil
}

// alsaDeviceRE matches a device line in aplay's and arecord's listing.
var alsaDeviceRE = regexp.MustCompile(`^card (\d+): ([\w-]+) \[([^\]]*)\], device (\d+): (.+)$`)

// listALSAUtilsDevices reads the devices from alsa-utils.
//
// aplay and arecord name each device the way the mixer does, which is
// what a person recognises. The kernel's list gives the same numbers
// with less of a name.
func listALSAUtilsDevices(cards map[int]alsaCard) (inputs, outputs []Device, ok bool) {
	in, inOK := readALSAUtilsList("arecord", cards)
	out, outOK := readALSAUtilsList("aplay", cards)
	if !inOK && !outOK {
		return nil, nil, false
	}
	return in, out, true
}

// readALSAUtilsList reads one direction's devices from alsa-utils.
func readALSAUtilsList(command string, cards map[int]alsaCard) ([]Device, bool) {
	out, err := exec.Command(command, "-l").Output()
	if err != nil {
		return nil, false
	}

	var devices []Device
	for _, line := range strings.Split(string(out), "\n") {
		m := alsaDeviceRE.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		card := atoi(m[1])
		device := atoi(m[4])

		name := alsaDeviceName(m[5])
		if name == "" {
			name = strings.TrimSpace(m[2])
		}

		description := name
		if cardInfo, found := cards[card]; found && cardInfo.Description != "" {
			description = fmt.Sprintf("%s (%s)", name, cardInfo.Description)
		}

		devices = append(devices, Device{
			Name:        fmt.Sprintf("%s:plughw:%d,%d", FormatALSA, card, device),
			Format:      FormatALSA,
			Target:      fmt.Sprintf("plughw:%d,%d", card, device),
			Description: description,
		})
	}
	if len(devices) == 0 {
		return nil, false
	}
	return devices, true
}

// procPCMRE matches a device line in /proc/asound/pcm.
var procPCMRE = regexp.MustCompile(`^(\d+)-(\d+): (.+)$`)

// procPCM is one device from the kernel's PCM list.
type procPCM struct {
	Device   Device
	Capture  bool
	Playback bool
}

// listProcPCMs reads the devices from the kernel's PCM list.
//
// It is the fallback for a machine without alsa-utils. The names it
// carries are the kernel's, which are short: "DMIC", "USB Audio".
func listProcPCMs(cards map[int]alsaCard) []procPCM {
	data, err := os.ReadFile("/proc/asound/pcm")
	if err != nil {
		return nil
	}

	var pcms []procPCM
	for _, line := range strings.Split(string(data), "\n") {
		m := procPCMRE.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		card := atoi(m[1])
		device := atoi(m[2])

		fields := strings.Fields(m[3])
		if len(fields) == 0 {
			continue
		}
		name := fields[0]

		description := name
		if cardInfo, found := cards[card]; found && cardInfo.Description != "" {
			description = fmt.Sprintf("%s (%s)", name, cardInfo.Description)
		}

		target := fmt.Sprintf("plughw:%d,%d", card, device)
		pcms = append(pcms, procPCM{
			Device: Device{
				Name:        FormatALSA + ":" + target,
				Format:      FormatALSA,
				Target:      target,
				Description: description,
			},
			Capture:  strings.Contains(m[3], "capture"),
			Playback: strings.Contains(m[3], "playback"),
		})
	}
	return pcms
}

// alsaDeviceName reads the device's name out of alsa-utils' listing.
//
// The listing gives `name [id]`, and marks the card's default device
// with `(*)` after the name.
func alsaDeviceName(raw string) string {
	name := strings.TrimSpace(raw)
	if i := strings.LastIndex(name, " ["); i >= 0 && strings.HasSuffix(name, "]") {
		name = strings.TrimSpace(name[:i])
	}
	name = strings.TrimSuffix(name, "(*)")
	return strings.TrimSpace(name)
}
