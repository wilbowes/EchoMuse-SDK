package emulator

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
)

// A device is named by two things: the format that reaches it and the
// device within that format.
//
// The format is ffmpeg's own, so a device is written the way ffmpeg
// writes it:
//
//	alsa:plughw:2,0
//	pulse:alsa_output.usb-Generic_USB_Audio-00.analog-stereo
//	avfoundation::default
//	dshow:audio=Microphone (2- High Definition Audio Device)
//
// A name with no format is read against the platform's default format,
// which is the one that reaches the system's default device.
//
// The default device needs no name at all, and that is the usual case:
// an empty device is the platform's default microphone and speaker, the
// way a browser takes them. Which device that is depends on the machine,
// and the machine's own mixer is the authority.
const (
	// DeviceDefault names the platform's default device.
	DeviceDefault = "default"

	// FormatALSA is ffmpeg's ALSA device format.
	FormatALSA = "alsa"
	// FormatPulse is ffmpeg's PulseAudio device format. It also reaches
	// PipeWire, which presents a PulseAudio-compatible socket.
	FormatPulse = "pulse"
	// FormatAVFoundation is ffmpeg's CoreAudio device format.
	FormatAVFoundation = "avfoundation"
	// FormatDSShow is ffmpeg's DirectShow device format.
	FormatDSShow = "dshow"
)

// Device is one input or output the emulator can use.
type Device struct {
	// Name is the device's full name, as it is written on a command
	// line: the format, then the device within it.
	Name string
	// Format is the format that reaches the device: alsa, pulse,
	// avfoundation or dshow.
	Format string
	// Target is the device within the format.
	Target string
	// Description is the machine's own label for it.
	Description string
	// Default is whether this is the system default for its direction.
	Default bool
}

// String renders the device as it would be written on a command line.
func (d Device) String() string { return d.Name }

// ParseDevice reads a device name.
//
// The name may name the format explicitly, as `pulse:foo` or
// `alsa:plughw:2,0`, or it may be left to the caller to interpret. A name
// with no format is read against the platform's default format.
func ParseDevice(raw string) Device {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultDevice()
	}

	format, target, found := strings.Cut(raw, ":")
	if !found {
		return Device{
			Name:        raw,
			Format:      defaultFormat(),
			Target:      raw,
			Description: "",
		}
	}

	return Device{
		Name:   raw,
		Format: format,
		Target: target,
	}
}

// defaultDevice is the platform's default device.
func defaultDevice() Device {
	format := defaultFormat()
	return Device{
		Name:        DeviceDefault,
		Format:      format,
		Target:      defaultTarget(format),
		Description: "the system default for this direction",
		Default:     true,
	}
}

// defaultFormat is the format that reaches the platform's default device.
//
// Linux has two, and the audio server's is the better one: a hardware
// ALSA device is exclusive, so a microphone on a card takes the card's
// speaker with it. The audio server mixes, so both can be open at once.
func defaultFormat() string {
	switch runtime.GOOS {
	case "darwin":
		return FormatAVFoundation
	case "windows":
		return FormatDSShow
	default:
		if hasPulse() {
			return FormatPulse
		}
		return FormatALSA
	}
}

// defaultTarget is the device within the format that means "the system's
// default".
func defaultTarget(format string) string {
	switch format {
	case FormatAVFoundation:
		// AVFoundation reads "video:audio", and an empty video with
		// "default" audio is the default microphone on its own.
		return ":default"
	case FormatDSShow:
		return "audio=default"
	default:
		return DeviceDefault
	}
}

// ListDevices returns the input and output devices the machine has.
//
// It is how a program that wants to offer a choice finds out what there
// is to choose. Every source is consulted and the results are merged,
// because a machine with both an audio server and ALSA has both sets: the
// server's names are the ones the mixer shows, and the ALSA names are the
// ones that work when the server is not running.
//
// A machine with no audio server has no server device names, and a
// machine with no ALSA has no cards. Both are reported as an empty list
// rather than as an error — the machine may still have the other kind.
func ListDevices() (inputs []Device, outputs []Device, err error) {
	switch runtime.GOOS {
	case "darwin":
		return listAVFoundationDevices()
	case "windows":
		return listDSShowDevices()
	default:
		return listLinuxDevices()
	}
}

// listLinuxDevices reads the ALSA and audio-server devices.
//
// The kernel's list gives the cards and alsa-utils names each device the
// way the mixer does, which is what a person recognises. The audio
// server's list gives the devices the server actually mixes.
func listLinuxDevices() (inputs, outputs []Device, err error) {
	inputs = append(inputs, defaultDevice())

	if in, out, ok := listPipeWireDevices(); ok {
		inputs = append(inputs, in...)
		outputs = append(outputs, out...)
	} else {
		in, out := listPulseDevices()
		inputs = append(inputs, in...)
		outputs = append(outputs, out...)
	}

	alsaIn, alsaOut, alsaErr := listALSADevices()
	if alsaErr == nil {
		inputs = append(inputs, alsaIn...)
		outputs = append(outputs, alsaOut...)
	}

	sortDevices(inputs)
	sortDevices(outputs)
	return inputs, outputs, nil
}

// listAVFoundationDevices reads the CoreAudio devices.
//
// ffmpeg is the only portable way to ask CoreAudio what it has, and it
// lists the devices in one listing with its own numbering.
func listAVFoundationDevices() (inputs, outputs []Device, err error) {
	listing, err := ffmpegDeviceList(FormatAVFoundation)
	if err != nil {
		return nil, nil, err
	}

	inputs = append(inputs, defaultDevice())
	for _, d := range listing {
		if d.audio {
			inputs = append(inputs, Device{
				Name:        FormatAVFoundation + ":" + d.target,
				Format:      FormatAVFoundation,
				Target:      d.target,
				Description: d.description,
			})
		} else {
			outputs = append(outputs, Device{
				Name:        FormatAVFoundation + ":" + d.target,
				Format:      FormatAVFoundation,
				Target:      d.target,
				Description: d.description,
			})
		}
	}

	sortDevices(inputs)
	sortDevices(outputs)
	return inputs, outputs, nil
}

// listDSShowDevices reads the DirectShow devices.
//
// DirectShow lists capture devices only, so there is no speaker list to
// read: the default output is the platform's.
func listDSShowDevices() (inputs, outputs []Device, err error) {
	listing, err := ffmpegDeviceList(FormatDSShow)
	if err != nil {
		return nil, nil, err
	}

	inputs = append(inputs, defaultDevice())
	for _, d := range listing {
		if !d.audio {
			continue
		}
		inputs = append(inputs, Device{
			Name:        FormatDSShow + ":" + d.target,
			Format:      FormatDSShow,
			Target:      d.target,
			Description: d.description,
		})
	}

	sortDevices(inputs)
	sortDevices(outputs)
	return inputs, outputs, nil
}

// ffmpegDevice is one device from an ffmpeg device listing.
type ffmpegDevice struct {
	// target is the device's name as ffmpeg writes it.
	target string
	// description is the device's own name.
	description string
	// audio is whether it is an audio device rather than a video one.
	audio bool
}

// ffmpegDeviceRE matches a device line in ffmpeg's listing.
//
// AVFoundation writes "[2] Microphone (2- High Definition Audio
// Device)" and DirectShow writes "\"Microphone (2- High Definition Audio
// Device)\" (audio)".
var ffmpegDeviceRE = regexp.MustCompile(`^(?:\[\d+\]\s+)?(?:"([^"]+)"|(.+?))\s*(\(audio\)|\(video\))?$`)

// ffmpegDeviceList asks ffmpeg what devices the platform has.
//
// ffmpeg writes the listing to its error output, because a listing with
// no input to read is an error as far as it is concerned.
func ffmpegDeviceList(format string) ([]ffmpegDevice, error) {
	out, err := exec.Command("ffmpeg", "-hide_banner", "-f", format, "-list_devices", "true", "-i", "").CombinedOutput()
	if err == nil {
		return nil, fmt.Errorf("ffmpeg listed no %s devices", format)
	}

	var devices []ffmpegDevice
	audioSection := false
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.Contains(line, "audio devices:") {
			audioSection = true
			continue
		}
		if strings.Contains(line, "video devices:") {
			audioSection = false
			continue
		}
		if !strings.Contains(line, "AVFoundation") && !strings.Contains(line, "dshow") {
			continue
		}

		// The line is "prefix: [2] Name" or "prefix: \"Name\" (audio)".
		if i := strings.Index(line, ": "); i >= 0 {
			line = strings.TrimSpace(line[i+2:])
		} else {
			continue
		}

		m := ffmpegDeviceRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}

		target := m[1]
		if target == "" {
			target = strings.TrimSpace(m[2])
		}
		if target == "" {
			continue
		}

		description := strings.TrimSpace(strings.Trim(target, "\""))
		devices = append(devices, ffmpegDevice{
			target:      target,
			description: description,
			audio:       audioSection || m[3] == "(audio)",
		})
	}
	return devices, nil
}

// hasPulse reports whether the machine runs PulseAudio or PipeWire.
//
// The socket is what matters: a PipeWire machine presents a
// PulseAudio-compatible socket, and ffmpeg's `pulse` format speaks to it.
func hasPulse() bool {
	for _, path := range []string{"/run/user/1000/pulse/native", "/run/pulse/native"} {
		if fileExists(path) {
			return true
		}
	}
	out, err := exec.Command("sh", "-c", "ls /run/user/*/pipewire-0 2>/dev/null | head -1").Output()
	if err == nil && strings.TrimSpace(string(out)) != "" {
		return true
	}
	return false
}

// pipeWireObject is one object from pw-dump.
//
// The props live under `info` for most objects and at the top level for
// the metadata, so both are read.
//
// The props are read as `any` rather than as strings because PipeWire
// types its values: a card number is an integer, a flag is a boolean and
// a channel map is a list. Decoding them as strings fails the whole
// object, and one integer would take the listing with it.
type pipeWireObject struct {
	ID    int            `json:"id"`
	Type  string         `json:"type"`
	Props map[string]any `json:"props"`
	Info  struct {
		Props map[string]any `json:"props"`
	} `json:"info"`
	Metadata []pipeWireMetadata `json:"metadata"`
}

// prop reads a property, wherever the object keeps it.
func (o pipeWireObject) prop(name string) string {
	if v, ok := o.Info.Props[name]; ok {
		return pipeWireValue(v)
	}
	return pipeWireValue(o.Props[name])
}

// pipeWireValue renders a property value as a string.
//
// A value that is not a string — a number, a flag — has no use as a
// device name, so it comes back empty.
func pipeWireValue(v any) string {
	s, _ := v.(string)
	return s
}

// pipeWireMetadata is one entry of a metadata object.
type pipeWireMetadata struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

// name reads the device name out of a metadata value.
//
// The value is a JSON object for the default keys and a plain string for
// others, so both are tried.
func (m pipeWireMetadata) name() string {
	var obj struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(m.Value, &obj); err == nil && obj.Name != "" {
		return obj.Name
	}
	var s string
	if err := json.Unmarshal(m.Value, &s); err == nil {
		return s
	}
	return ""
}

// listPipeWireDevices asks PipeWire what it has.
//
// It reads the server's own state through pw-dump, which is the only
// listing that carries the node's nick, its description and the server's
// default without a second tool.
//
// The format is `pulse` and not a name of its own: ffmpeg reaches
// PipeWire through the PulseAudio-compatible socket it presents.
func listPipeWireDevices() (inputs, outputs []Device, ok bool) {
	raw, err := exec.Command("pw-dump").Output()
	if err != nil {
		return nil, nil, false
	}

	var objects []pipeWireObject
	if err := json.Unmarshal(raw, &objects); err != nil {
		return nil, nil, false
	}

	defaultSource, defaultSink := "", ""
	for _, o := range objects {
		if o.Type != "PipeWire:Interface:Metadata" {
			continue
		}
		if o.prop("metadata.name") != "default" {
			continue
		}
		for _, m := range o.Metadata {
			switch m.Key {
			case "default.audio.source":
				defaultSource = m.name()
			case "default.audio.sink":
				defaultSink = m.name()
			}
		}
	}

	for _, o := range objects {
		if o.Type != "PipeWire:Interface:Node" {
			continue
		}
		name := o.prop("node.name")
		if name == "" {
			continue
		}

		description := o.prop("node.nick")
		if description == "" {
			description = o.prop("node.description")
		}

		switch o.prop("media.class") {
		case "Audio/Source":
			inputs = append(inputs, Device{
				Name:        FormatPulse + ":" + name,
				Format:      FormatPulse,
				Target:      name,
				Description: description,
				Default:     name == defaultSource,
			})
		case "Audio/Sink":
			outputs = append(outputs, Device{
				Name:        FormatPulse + ":" + name,
				Format:      FormatPulse,
				Target:      name,
				Description: description,
				Default:     name == defaultSink,
			})
		}
	}

	if len(inputs) == 0 && len(outputs) == 0 {
		return nil, nil, false
	}
	return inputs, outputs, true
}

// listPulseDevices asks PulseAudio what it has.
//
// It is the fallback for a machine that runs PulseAudio without
// PipeWire's tools.
func listPulseDevices() (inputs, outputs []Device) {
	defaultSink := pulseDefault("get-default-sink")
	defaultSource := pulseDefault("get-default-source")

	for _, d := range pulseList("sinks") {
		d.Default = d.Target == defaultSink
		outputs = append(outputs, d)
	}
	for _, d := range pulseList("sources") {
		d.Default = d.Target == defaultSource
		inputs = append(inputs, d)
	}
	return inputs, outputs
}

// pulseDefault asks PulseAudio for the default device of a direction.
func pulseDefault(which string) string {
	out, err := exec.Command("pactl", which).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// pulseList reads PulseAudio's devices of one direction.
//
// The short listing gives the names, and the full listing gives the
// descriptions, so both are read and matched.
func pulseList(direction string) []Device {
	short, err := exec.Command("pactl", "list", "short", direction).Output()
	if err != nil {
		return nil
	}

	descriptions := map[string]string{}
	full, err := exec.Command("pactl", "list", direction).Output()
	if err == nil {
		descriptions = parsePulseDescriptions(string(full))
	}

	var devices []Device
	for _, line := range strings.Split(string(short), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) == 0 {
			continue
		}
		name := fields[0]
		devices = append(devices, Device{
			Name:        FormatPulse + ":" + name,
			Format:      FormatPulse,
			Target:      name,
			Description: descriptions[name],
		})
	}
	return devices
}

// parsePulseDescriptions reads the descriptions out of `pactl list`.
//
// The output is a run of blocks separated by blank lines, each with a
// `Name:` and a `Description:` line.
func parsePulseDescriptions(text string) map[string]string {
	descriptions := map[string]string{}
	for _, block := range strings.Split(text, "\n\n") {
		name, description := "", ""
		for _, line := range strings.Split(block, "\n") {
			line = strings.TrimSpace(line)
			switch {
			case strings.HasPrefix(line, "Name:"):
				name = strings.TrimSpace(strings.TrimPrefix(line, "Name:"))
			case strings.HasPrefix(line, "Description:"):
				description = strings.TrimSpace(strings.TrimPrefix(line, "Description:"))
			}
		}
		if name != "" {
			descriptions[name] = description
		}
	}
	return descriptions
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

// sortDevices orders devices by name, so a list is stable between runs.
func sortDevices(devices []Device) {
	sort.Slice(devices, func(i, j int) bool {
		return devices[i].Name < devices[j].Name
	})
}

// resolveDevice turns a device into the ffmpeg arguments that reach it.
//
// The format matters as much as the name: ffmpeg's `default` is an ALSA
// device, and a PulseAudio device is reached through the `pulse` format
// instead.
//
// The format prefix is the SDK's own, so it is stripped here: ffmpeg's
// ALSA device is `plughw:2,0`, not `alsa:plughw:2,0`.
func resolveDevice(d Device) (format string, target string) {
	format = d.Format
	if format == "" {
		format = defaultFormat()
	}

	switch format {
	case FormatALSA:
		return FormatALSA, alsaName(d.Target)
	case FormatPulse:
		return FormatPulse, d.Target
	case FormatAVFoundation:
		return FormatAVFoundation, d.Target
	case FormatDSShow:
		return FormatDSShow, d.Target
	default:
		return format, d.Target
	}
}

// alsaName turns an ALSA device name into one ffmpeg can open.
//
// The protocol's audio is mono. A hardware device that only does stereo
// refuses a mono stream outright — ffmpeg reports "cannot set channel
// count to 1" and the speaker goes silent — so a hardware name is opened
// through the plug, which does the conversion.
//
// `default` is left alone: it is already the plug's own name.
func alsaName(name string) string {
	name = strings.TrimPrefix(name, FormatALSA+":")
	if strings.HasPrefix(name, "hw:") {
		return "plughw:" + strings.TrimPrefix(name, "hw:")
	}
	return name
}

// describeDevice renders a device for the window.
func describeDevice(d Device) string {
	if d.Description == "" {
		return d.Name
	}
	return fmt.Sprintf("%s (%s)", d.Name, d.Description)
}

// fileExists reports whether a path is present.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// atoi reads an integer, or zero when the text is not one.
func atoi(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// stderrBuffer collects a process's error output.
//
// The reason a sound card refused the audio is on ffmpeg's error output
// and nowhere else: the write to its pipe fails first, and the pipe's
// error says only that the pipe broke.
type stderrBuffer struct {
	mu  sync.Mutex
	buf []byte
}

// Write collects the output.
func (s *stderrBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buf = append(s.buf, p...)
	if len(s.buf) > 8192 {
		s.buf = s.buf[len(s.buf)-8192:]
	}
	return len(p), nil
}

// Tail returns the last few lines of the output, which is where the
// reason is.
func (s *stderrBuffer) Tail() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	var lines []string
	for _, line := range strings.Split(string(s.buf), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) > 3 {
		lines = lines[len(lines)-3:]
	}
	return strings.Join(lines, " ")
}
