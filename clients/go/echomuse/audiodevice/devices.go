// Package audiodevice finds the audio endpoints a device can use.
//
// The names it produces are opaque to the SDK and are handed back
// unchanged to the backend that opens them. A program that shows them to a
// person needs them to look like the names the mixer uses, so the listing
// comes from alsa-utils when it is installed and from the kernel otherwise.
//
// The listing is pure Go and reads no audio hardware, so it works on a
// device with no sound card and on a build machine.
package audiodevice

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
)

const (
	// DeviceDefault is the name of the system's default endpoint.
	DeviceDefault = "default"
	// FormatALSA is the format every name carries on Linux.
	FormatALSA = "alsa"
)

// Endpoint is one audio endpoint.
//
// The Name is what a backend is given to open it. The Description is what a
// person recognises, and the Default says whether it is the system's own
// choice.
type Endpoint struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Default     bool   `json:"default,omitempty"`
}

// List returns the capture and playback endpoints on this machine.
//
// The system default is always in each list, because it is what a device
// uses when nobody has chosen anything else.
//
// It returns an error when the machine has no ALSA at all, which is a
// normal thing for a build machine or a container to be.
func List() (inputs, outputs []Endpoint, err error) {
	in, out, listErr := listALSADevices()
	if listErr != nil {
		return nil, nil, listErr
	}

	inputs = append(inputs, Endpoint{Name: DeviceDefault, Description: "System default", Default: true})
	outputs = append(outputs, Endpoint{Name: DeviceDefault, Description: "System default", Default: true})
	for _, d := range in {
		inputs = append(inputs, Endpoint{Name: d.Name, Description: d.Description, Default: d.Default})
	}
	for _, d := range out {
		outputs = append(outputs, Endpoint{Name: d.Name, Description: d.Description, Default: d.Default})
	}

	sort.Slice(inputs, func(i, j int) bool { return inputs[i].Name < inputs[j].Name })
	sort.Slice(outputs, func(i, j int) bool { return outputs[i].Name < outputs[j].Name })
	return inputs, outputs, nil
}

// Device is one endpoint as the kernel and the mixer describe it.
type Device struct {
	Name        string
	Format      string
	Target      string
	Description string
	Default     bool
}

// String names the device.
func (d Device) String() string { return d.Name }

// ParseDevice turns a name into the device it refers to.
//
// An empty name is the system default, and a name with no format is ALSA,
// because that is the only format this package produces.
func ParseDevice(raw string) Device {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultDevice()
	}
	raw = strings.TrimPrefix(raw, FormatALSA+":")
	return Device{Name: FormatALSA + ":" + raw, Format: FormatALSA, Target: raw}
}

// defaultDevice is the system's own choice.
func defaultDevice() Device {
	return Device{Name: DeviceDefault, Format: FormatALSA, Target: DeviceDefault, Description: "System default", Default: true}
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

// alsaCard is one card, as the kernel names it.
type alsaCard struct {
	// ID is the card's short name, which is what `hw:CARD=` takes.
	ID string
	// Description is what the card is.
	Description string
	// LongName is the hardware's own name, when the kernel gives one.
	LongName string
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

// alsaCardRE matches a card line in /proc/asound/cards.
var alsaCardRE = regexp.MustCompile(`^\s*(\d+)\s+\[([^\]]+)\]:\s*(.*)$`)

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
		if cardInfo, found := cards[card]; found {
			description = cardInfo.Description + ": " + name
		}

		devices = append(devices, Device{
			Name:        fmt.Sprintf("alsa:%s,%d", strings.TrimSpace(m[2]), device),
			Format:      FormatALSA,
			Target:      fmt.Sprintf("hw:CARD=%s,DEV=%d", strings.TrimSpace(m[2]), device),
			Description: description,
		})
	}
	return devices, len(devices) > 0
}

// alsaDeviceName pulls the name out of a device description.
//
// The mixer writes it as "USB Audio: USB Audio (hw:1,0)" — the part before
// the parenthesis is the device's name and the part inside is the hardware
// one.
func alsaDeviceName(description string) string {
	before, _, found := strings.Cut(description, "(")
	if !found {
		return strings.TrimSpace(description)
	}
	return strings.TrimSpace(before)
}

// procPCM is one line of the kernel's PCM list.
type procPCM struct {
	Device   Device
	Capture  bool
	Playback bool
}

// procPCMRE matches a line of /proc/asound/pcm.
var procPCMRE = regexp.MustCompile(`^(\d+): \[(\d+):(\d+)]: (.*)$`)

// listProcPCMs reads the devices from the kernel.
//
// The kernel names a device by its numbers, which is not what a person
// recognises, but it is always there.
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
		card := atoi(m[2])
		device := atoi(m[3])

		name := strings.TrimSpace(m[4])
		// The name carries the directions: the capture and playback ones
		// are marked.
		capture := strings.Contains(name, ": capture")
		playback := strings.Contains(name, ": playback")

		cardID := ""
		description := name
		if cardInfo, found := cards[card]; found {
			cardID = cardInfo.ID
			description = cardInfo.Description + ": " + name
		}

		pcms = append(pcms, procPCM{
			Device: Device{
				Name:        fmt.Sprintf("alsa:%s,%d", cardID, device),
				Format:      FormatALSA,
				Target:      fmt.Sprintf("hw:CARD=%s,DEV=%d", cardID, device),
				Description: description,
			},
			Capture:  capture,
			Playback: playback,
		})
	}
	return pcms
}

// atoi reads the leading digits of a string.
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
