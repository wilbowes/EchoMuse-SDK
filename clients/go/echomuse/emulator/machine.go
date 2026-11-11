package emulator

// The emulator's own description of the machine it runs on.
//
// The controller compares a device's reported firmware version against the
// published releases and offers an update when the device is behind. The
// emulator is not an Echo and has no update slot, so it reports a version
// ahead of every release and the controller leaves it alone.

// kernelDescription is the running kernel in a form an operator can read.
//
// It is empty on a machine that does not say, which is a different answer
// from a kernel with no name.
func (e *Emulator) kernelDescription() string {
	switch {
	case e.kernelArch != "" && e.kernelRelease != "":
		return e.kernelArch + " " + e.kernelRelease
	case e.kernelArch != "":
		return e.kernelArch
	default:
		return e.kernelRelease
	}
}

// firmwareVersion is the version the emulator reports to the controller.
//
// It is the configured one, and the default is a version ahead of every
// published release — because the controller offers an update when a
// device is behind, and the emulator cannot take one.
func (e *Emulator) firmwareVersion() string {
	if e.opts.FirmwareVersion != "" {
		return e.opts.FirmwareVersion
	}
	return "v999.0.0"
}
