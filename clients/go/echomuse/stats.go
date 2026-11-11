package echomuse

import (
	"context"
	"time"

	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/protocol"
)

// defaultStatsInterval is how often the telemetry is reported.
//
// Thirty seconds is what the controller expects, and it is often enough for
// a dashboard to be live and rare enough not to be a burden on a device
// that is trying not to write to its flash.
const defaultStatsInterval = 30 * time.Second

// startStats reports the telemetry for as long as the context lasts.
//
// It is called once per connection, so a device that reconnects reports
// again once it is back — which is what the controller wants, because a
// device that was off the network has no fresh measurements until it
// sends some.
//
// The source is called on its own goroutine and the result is sent on the
// control plane. A source that blocks holds this goroutine and nothing
// else: the link, the audio and the callbacks all run elsewhere.
//
// A source that panics takes the process with it, so a source is the
// application's own code and is expected to be careful.
func (d *Device) startStats(ctx context.Context) {
	if d.opts.StatsSource == nil {
		return
	}
	interval := d.opts.StatsInterval
	if interval <= 0 {
		interval = defaultStatsInterval
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				d.reportStats()
			}
		}
	}()
}

// reportStats takes one reading and sends it.
//
// A source that fails is not fatal: the SDK does not know what a reading
// is, so it cannot tell a failure from an absent measurement. A source
// that cannot read anything returns a zero-valued Stats, whose fields are
// all absent — which the controller reads as "not measured", exactly as it
// should.
func (d *Device) reportStats() {
	stats := d.opts.StatsSource()
	if err := d.SendStats(stats); err != nil {
		d.log.Debug("the telemetry did not reach the controller", "error", err)
	}
}

// Stats returns the telemetry the SDK would report right now.
//
// It is the same source the periodic report uses, so an application can
// read the current values without waiting for the tick. A device with no
// source gets an empty Stats, whose fields are absent rather than zero.
func (d *Device) Stats() protocol.Stats {
	if d.opts.StatsSource == nil {
		return protocol.Stats{}
	}
	return d.opts.StatsSource()
}

// firmwareVersion is the version the device reports to the controller.
//
// It is the firmware version when the application has one, and the
// application's own version when it does not. The two are separate because
// a device that is not updated by the controller still has to answer the
// question the controller asks — "is this device behind the published
// firmware?" — and the honest answer for a device the controller cannot
// update is the version it is on.
func (d *Device) firmwareVersion() string {
	if d.opts.FirmwareVersion != "" {
		return d.opts.FirmwareVersion
	}
	return d.opts.Version
}
