package emulator

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/protocol"
)

// The emulator is the SDK's demonstration of a complete device: it produces
// the telemetry a real device produces, reports the flash wear, and answers
// the controller's questions about the network.
//
// Everything here is a working implementation, not a stub. An integrator
// reads it to see what a device has to do, and writes their own against
// the SDK's seams.

// Telemetry is the source of the emulator's periodic report.
//
// Every measurement is optional and absent means "not measured" — the
// SDK's rule, and the controller's: a missing measurement and a measured
// zero are different answers.
type Telemetry struct {
	// WiFiSSID is the network the device is on.
	WiFiSSID string
	// WiFiBSSID is the access point's address.
	WiFiBSSID string
	// WiFiRSSI is the signal strength in dBm. Nil means not measured.
	WiFiRSSI *int
	// WiFiFreqMhz is the channel's centre frequency.
	WiFiFreqMhz *int
	// LinkSpeedMbps is the link's speed. Nil means not measured.
	LinkSpeedMbps *int

	// StorageTotalMb is the size of the filesystem the device runs on.
	StorageTotalMb int
	// StorageUsedMb is how much of it is in use.
	StorageUsedMb int

	// CPUTempC is the processor's temperature. Nil means not measured.
	CPUTempC *float64
	// MaxTempC is the hottest core.
	MaxTempC *float64

	// EMMC is the flash wear report, keyed as the controller reads it.
	EMMC map[string]any
}

// Sample takes one reading.
//
// It is a real measurement of this machine: the CPU share is the Go
// runtime's own, the memory is the process's, and the storage is the
// filesystem the emulator is running on. The values this machine cannot
// measure are left absent rather than zeroed.
func (t Telemetry) Sample() protocol.Stats {
	stats := protocol.Stats{}

	// The CPU share is over the online cores, so a machine that has
	// hotplugged a core does not read as idle.
	cores := runtime.NumCPU()
	if cores < 1 {
		cores = 1
	}
	online := cores
	stats.CoresOnline = &online
	stats.CoresTotal = &cores
	stats.CPUPct = cpuPercent()

	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	// The memory is the MACHINE's, not this process's.
	//
	// The firmware reads /proc/meminfo, and so does the emulator: the
	// dashboard's RAM figure is the device's memory, and a number that
	// means "the Go heap" would be a different thing wearing the same
	// label. A panel with 2 GB shows 2 GB, not the 12 MB its process
	// happens to have reserved.
	usedMb, totalMb := readMemInfo()
	if totalMb > 0 {
		stats.MemUsedMb = usedMb
		stats.MemTotalMb = totalMb
	} else {
		// A machine with no /proc/meminfo to read. The process's own
		// heap is the next best answer, and it is an answer.
		stats.MemUsedMb = int(mem.Alloc / (1024 * 1024))
		stats.MemTotalMb = int(mem.Sys / (1024 * 1024))
	}

	if t.StorageTotalMb > 0 {
		stats.StorageUsedMb = t.StorageUsedMb
		stats.StorageTotalMb = t.StorageTotalMb
	}

	if t.WiFiSSID != "" {
		stats.WifiSsid = t.WiFiSSID
	}
	if t.WiFiBSSID != "" {
		stats.WifiBssid = t.WiFiBSSID
	}
	if t.WiFiRSSI != nil {
		stats.WifiRssi = t.WiFiRSSI
	}
	if t.WiFiFreqMhz != nil {
		stats.WifiFreqMhz = t.WiFiFreqMhz
	}
	if t.LinkSpeedMbps != nil {
		stats.LinkSpeedMbps = t.LinkSpeedMbps
	}
	if t.CPUTempC != nil {
		stats.CPUTempC = t.CPUTempC
	}
	if t.MaxTempC != nil {
		stats.MaxTempC = t.MaxTempC
	}
	if len(t.EMMC) > 0 {
		stats.EMMC = t.EMMC
	}
	return stats
}

// cpuPercent is the share of the processor this process is using.
//
// It is the process's own CPU time over the wall-clock interval since the
// last reading, as a share of the online cores.
//
// The first reading has no interval behind it, and it reports the
// process's total CPU time since it started against the time it has been
// alive. That is not the same measurement as the later ones, so the
// counters are read once at the start and every reading after that is
// over the interval since the last one.
func cpuPercent() float64 {
	now := time.Now()
	previous := lastCPU
	lastCPU = now

	usage, err := processCPUTime()
	if err != nil {
		return 0
	}
	if previous.IsZero() {
		return 0
	}
	elapsed := now.Sub(previous).Seconds()
	if elapsed <= 0 {
		return 0
	}
	used := usage - lastUsage
	lastUsage = usage
	if used < 0 {
		// The counters went backwards, which they do not, but a clock
		// that did would otherwise produce a negative share.
		return 0
	}
	cores := runtime.NumCPU()
	if cores < 1 {
		cores = 1
	}
	pct := (used / elapsed) * 100 / float64(cores)
	if pct < 0 {
		return 0
	}
	if pct > 100 {
		return 100
	}
	return pct
}

// processCPUTime is the process's CPU time in seconds.
//
// It counts every thread, and it is the same number the kernel keeps for
// the whole process.
func processCPUTime() (float64, error) {
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		return 0, err
	}
	return float64(usage.Utime.Sec) + float64(usage.Utime.Usec)/1e6 +
		float64(usage.Stime.Sec) + float64(usage.Stime.Usec)/1e6, nil
}

// The CPU counters, kept between readings.
var (
	lastCPU time.Time
	// lastUsage is the CPU time at the last reading.
	lastUsage float64
)

// readMemInfo reads the machine's memory from /proc/meminfo.
//
// It returns the used and total megabytes. The used figure is the total
// less what is available, which is what the firmware reports and what
// the dashboard's RAM bar means.
func readMemInfo() (usedMb, totalMb int) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	var totalKb, availKb uint64
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		switch fields[0] {
		case "MemTotal:":
			totalKb = value
		case "MemAvailable:":
			availKb = value
		}
	}
	if totalKb == 0 {
		return 0, 0
	}
	return int((totalKb - availKb) / 1024), int(totalKb / 1024)
}

// ReadBootID reads the kernel's boot id.
//
// It is the same file the firmware reads, and a device that cannot read it
// has no boot id — which is a different answer from a boot id of "".
func ReadBootID() string {
	return readProc("/proc/sys/kernel/random/boot_id")
}

// ReadKernel reads the running kernel's architecture and release.
//
// They are `uname -m` and `uname -r`, and both are absent when the files
// are not there to ask.
func ReadKernel() (arch, release string) {
	return readProc("/proc/sys/kernel/arch"), readProc("/proc/sys/kernel/osrelease")
}

// readProc reads a single-line file under /proc.
func readProc(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// ReadStorage measures the filesystem the emulator is running on.
//
// It returns the total and used megabytes. A filesystem that cannot be
// measured returns zeros, and the caller leaves the fields out.
func ReadStorage(path string) (totalMb, usedMb int) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, 0
	}
	blockSize := int64(stat.Bsize)
	if blockSize <= 0 {
		return 0, 0
	}
	total := stat.Blocks * uint64(blockSize)
	used := (stat.Blocks - stat.Bfree) * uint64(blockSize)
	return int(total / (1024 * 1024)), int(used / (1024 * 1024))
}

// ReadEMMC reads the flash wear report.
//
// The keys are the ones the controller reads: rev, preEol, lifeA, lifeB,
// name, date and manfid. A device with no eMMC to ask returns nothing, and
// the controller stores NULL rather than a zero that would read as a new
// flash.
func ReadEMMC() map[string]any {
	const sysfs = "/sys/class/mmc_host"
	entries, err := os.ReadDir(sysfs)
	if err != nil || len(entries) == 0 {
		return nil
	}
	// The first eMMC host is the one. A machine with two is not a case the
	// controller has an answer for either.
	host := filepath.Join(sysfs, entries[0].Name())
	out := map[string]any{}
	if v := readProc(filepath.Join(host, "name")); v != "" {
		out["name"] = v
	}
	if v := readProc(filepath.Join(host, "manfid")); v != "" {
		out["manfid"] = v
	}
	if v := readProc(filepath.Join(host, "date")); v != "" {
		out["date"] = v
	}
	if v := readProc(filepath.Join(host, "rev")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			out["rev"] = n
		}
	}
	if v := readProc(filepath.Join(host, "preEol")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			out["preEol"] = n
		}
	}
	if v := readProc(filepath.Join(host, "life_time")); v != "" {
		// The kernel writes "a b", the two lifetimes.
		fields := strings.Fields(v)
		if len(fields) == 2 {
			if a, err := strconv.Atoi(fields[0]); err == nil {
				out["lifeA"] = a
			}
			if b, err := strconv.Atoi(fields[1]); err == nil {
				out["lifeB"] = b
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
