package echomuse

import (
	"context"

	"github.com/gorilla/websocket"

	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/protocol"
)

// serveData reads the data plane until it drops.
//
// The data plane carries playback in one direction and capture in the
// other. The frame codes are namespaced by direction, so the same byte
// means different things depending on who sent it — which is why the
// dispatch below is explicit about the two tables rather than reading one
// global one.
func (d *Device) serveData(ctx context.Context, p *plane) {
	defer p.Close()

	go d.answerPings(ctx, p)

	for {
		msgType, data, err := p.conn.ReadMessage()
		if err != nil {
			if ctx.Err() == nil {
				d.log.Debug("data plane closed", "error", err)
			}
			return
		}
		p.refreshDeadline()
		if msgType != websocket.BinaryMessage {
			continue
		}
		if len(data) == 0 {
			continue
		}

		switch data[0] {
		case protocol.FrameSpeaker:
			if d.playbackBytes == 0 {
				d.log.Debug("playback started")
			}
			d.playbackBytes += int64(len(data) - 1)
			d.emitSpeaker(data[1:])
			d.state.SetSpeaking(true)
		case protocol.FrameSpeakerEOS:
			d.log.Debug("playback finished", "bytes", d.playbackBytes)
			d.playbackBytes = 0
			d.state.SetSpeaking(false)
			d.emitSpeakerEOS()
		case protocol.FrameMusic:
			d.emitMusic(data[1:])
		case protocol.FrameMusicEOS:
			d.emitMusicEOS()
		case protocol.FrameBLEGATT:
			d.log.Debug("Bluetooth frame received", "bytes", len(data)-1)
		default:
			d.log.Debug("unknown frame from the controller", "type", data[0])
		}
	}
}
