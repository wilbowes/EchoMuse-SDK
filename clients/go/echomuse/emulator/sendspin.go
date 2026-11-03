package emulator

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"time"

	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/protocol"
	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/sendspin"
)

const sendspinStoreName = "sendspin.json"

func (e *Emulator) applySendspinConfig(cfg protocol.Config) {
	if cfg.SendspinEnabled == nil {
		return
	}
	if !*cfg.SendspinEnabled {
		e.stopSendspin("user_request")
		_ = e.device.SendSendspinStatus(nil)
		return
	}

	e.mu.Lock()
	current := e.sendspin
	name := e.sendspinName
	if cfg.SendspinName != nil {
		name = strings.TrimSpace(*cfg.SendspinName)
	}
	if name == "" {
		name = "EchoMuse " + suffix(e.opts.DeviceID, 4)
	}
	unpaired := e.sendspinUnpaired
	if cfg.SendspinUnpaired != nil {
		unpaired = *cfg.SendspinUnpaired
		e.sendspinUnpaired = unpaired
	}
	restart := current != nil && e.sendspinName != name
	if restart {
		e.sendspin = nil
		e.sendspinName = ""
	}
	e.mu.Unlock()
	instance := "echomuse-" + safeInstance(e.opts.DeviceID)
	if restart {
		current.Stop("restart")
		current = nil
	}

	if current == nil {
		player, err := sendspin.New(sendspin.Config{
			StorePath: filepath.Join(e.opts.CredentialsDir, sendspinStoreName),
			Name:      name, Instance: instance, Product: "EchoMuse Emulator", Version: "0.1.0",
			Unpaired: unpaired,
			// Shared ALSA output uses a modest lead to keep startup responsive;
			// keep jitter buffers small so it does not add seconds of latency.
			RequiredLeadTime: 250 * time.Millisecond,
			MinBuffer:        150 * time.Millisecond,
		})
		if err == nil {
			player.OnVolume(func(level int) {
				go func() {
					if err := e.SetVolume(level); err != nil {
						e.log.Warn("Sendspin volume update failed", "error", err)
					}
				}()
			})
			player.OnSettings(func(_ int, muted bool, _ int) {
				e.mu.Lock()
				e.sendspinMuted = muted
				e.mu.Unlock()
			})
			e.mu.Lock()
			volume := e.volume
			e.mu.Unlock()
			player.SetVolume(volume)
			err = player.Start()
		}
		if err != nil {
			e.log.Error("Sendspin player could not start", "error", err)
			_ = e.device.SendSendspinStatus(map[string]string{"state": "error", "error": err.Error()})
			return
		}
		player.OnChange(func() { _ = e.device.SendSendspinStatus(player.Status()) })
		e.mu.Lock()
		e.sendspin = player
		e.sendspinName = name
		e.mu.Unlock()
		_ = e.device.SendSendspinStatus(player.Status())
		current = player
	}
	if current != nil {
		current.SetUnpaired(unpaired)
	}
}

func (e *Emulator) stopSendspin(reason string) {
	e.mu.Lock()
	c := e.sendspin
	e.sendspin = nil
	e.sendspinName = ""
	e.mu.Unlock()
	if c != nil {
		c.Stop(reason)
	}
}

func (e *Emulator) sendSendspinToken() {
	e.mu.Lock()
	c := e.sendspin
	e.mu.Unlock()
	if c == nil {
		_ = e.device.SendSendspinTokenResponse("", "", "Sendspin is disabled")
		return
	}
	if err := e.device.SendSendspinTokenResponse(c.PairingToken(), c.ClientID(), ""); err != nil {
		e.log.Warn("Sendspin token response failed", "error", err)
	}
}

// sendspinPump feeds Sendspin periods to the same playback stream used by
// voice responses unless an application provides a clocked output backend.
func (e *Emulator) sendspinPump(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	defer e.closeSendspinOutput()
	stereo := make([]byte, 48000/100*4) // 10ms, stereo S16LE
	mono := make([]byte, 48000/100*2)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		e.mu.Lock()
		c, voiceActive, muted, volume := e.sendspin, e.voiceActive, e.sendspinMuted, e.volume
		ducked, duckDB := e.musicDucked, e.duckDB
		speaker := e.speaker
		openAudio := speaker == nil && time.Since(e.lastSendspinAttempt) >= time.Second
		if openAudio {
			e.lastSendspinAttempt = time.Now()
		}
		e.mu.Unlock()
		if voiceActive && e.voiceMixAvailable() && speaker == nil {
			e.StartPlayback(ctx)
			e.mu.Lock()
			speaker = e.speaker
			e.mu.Unlock()
		}
		musicReady := c != nil && c.Active()
		if !musicReady && !(voiceActive && e.voiceMixAvailable()) {
			e.closeSendspinOutput()
			if !voiceActive {
				e.StopPlayback()
			}
			continue
		}
		if e.sendspinOutput != nil && c != nil {
			e.mu.Lock()
			clocked := e.sendspinOut
			openClocked := clocked == nil && time.Since(e.lastClockedAttempt) >= time.Second
			if openClocked {
				e.lastClockedAttempt = time.Now()
			}
			e.mu.Unlock()
			if openClocked {
				var err error
				clocked, err = e.sendspinOutput.OpenClockedOutput(ctx, 48000, 2)
				if err != nil {
					e.log.Warn("clocked Sendspin output unavailable; using shared audio output", "error", err)
					e.mu.Lock()
					e.sendspinOutput = nil
					e.mu.Unlock()
				} else {
					e.mu.Lock()
					e.sendspinOut = clocked
					e.mu.Unlock()
					e.log.Info("Sendspin is using the clocked audio output")
				}
			}
			if clocked != nil {
				playAt, uncertainty, err := clocked.NextPlaybackTime()
				if err == nil {
					clear(stereo)
					c.Fill(stereo, playAt, uncertainty)
					applySendspinGain(stereo, volume, muted, ducked, duckDB)
					if voiceActive && e.voiceMixAvailable() {
						e.mixVoiceIntoStereo(stereo)
					}
					if _, err = clocked.Write(stereo); err != nil {
						e.log.Warn("clocked Sendspin output write failed", "error", err)
						e.closeSendspinOutput()
					}
					continue
				}
				e.log.Warn("clocked Sendspin output clock failed", "error", err)
				e.disableClockedSendspinOutput()
			}
		}
		if openAudio {
			e.StartPlayback(ctx)
		}
		e.mu.Lock()
		speaker = e.speaker
		e.mu.Unlock()
		if speaker == nil {
			continue
		}
		if musicReady {
			clear(stereo)
			c.Fill(stereo, time.Now().Add(80*time.Millisecond), 2*time.Millisecond)
			applySendspinGain(stereo, volume, muted, ducked, duckDB)
			for i := 0; i+3 < len(stereo); i += 4 {
				left := int32(int16(uint16(stereo[i]) | uint16(stereo[i+1])<<8))
				right := int32(int16(uint16(stereo[i+2]) | uint16(stereo[i+3])<<8))
				v := (left + right) / 2
				mono[i/2], mono[i/2+1] = byte(v), byte(uint16(v)>>8)
			}
		} else {
			clear(mono)
		}
		if voiceActive && e.voiceMixAvailable() {
			e.mixVoiceInto(mono)
		}
		if _, err := speaker.Write(mono); err != nil {
			e.log.Warn("the speaker missed a Sendspin period", "error", err)
		}
	}
}

func (e *Emulator) setVoiceActive(busy bool) {
	e.mu.Lock()
	if busy {
		if !e.voiceActive {
			e.voiceMixed = e.sendspin != nil && e.sendspin.Active()
		}
		e.voiceActive = true
	} else {
		e.voiceActive = false
		e.voiceMixed = false
	}
	e.mu.Unlock()
}

func (e *Emulator) voiceMixAvailable() bool { e.mu.Lock(); defer e.mu.Unlock(); return e.voiceMixed }

func (e *Emulator) closeSendspinOutput() {
	e.mu.Lock()
	out := e.sendspinOut
	e.sendspinOut = nil
	e.mu.Unlock()
	if out != nil {
		_ = out.Close()
	}
}

func (e *Emulator) disableClockedSendspinOutput() {
	e.closeSendspinOutput()
	e.mu.Lock()
	e.sendspinOutput = nil
	e.mu.Unlock()
}

func applySendspinGain(pcm []byte, volume int, muted, ducked bool, duckDB float64) {
	duckGain := 1.0
	if ducked {
		duckGain = math.Pow(10, duckDB/20)
	}
	for i := 0; i+1 < len(pcm); i += 2 {
		v := float64(int16(uint16(pcm[i]) | uint16(pcm[i+1])<<8))
		if muted {
			v = 0
		} else {
			v = v * float64(volume) / 100 * duckGain
		}
		if v > 32767 {
			v = 32767
		}
		if v < -32768 {
			v = -32768
		}
		sample := int16(v)
		pcm[i], pcm[i+1] = byte(sample), byte(uint16(sample)>>8)
	}
}

func (e *Emulator) mixVoiceInto(music []byte) {
	voice := make([]byte, len(music))
	e.takeVoiceMix(voice)
	for i := 0; i+1 < len(music); i += 2 {
		m := int32(int16(uint16(music[i]) | uint16(music[i+1])<<8))
		v := int32(int16(uint16(voice[i]) | uint16(voice[i+1])<<8))
		sum := m + v
		if sum > 32767 {
			sum = 32767
		}
		if sum < -32768 {
			sum = -32768
		}
		sample := int16(sum)
		music[i], music[i+1] = byte(sample), byte(uint16(sample)>>8)
	}
}

func (e *Emulator) mixVoiceIntoStereo(music []byte) {
	voice := make([]byte, len(music)/2)
	e.takeVoiceMix(voice)
	for i := 0; i+3 < len(music); i += 4 {
		v := int32(int16(uint16(voice[i/2]) | uint16(voice[i/2+1])<<8))
		for _, j := range []int{i, i + 2} {
			sum := int32(int16(uint16(music[j])|uint16(music[j+1])<<8)) + v
			if sum > 32767 {
				sum = 32767
			}
			if sum < -32768 {
				sum = -32768
			}
			sample := int16(sum)
			music[j], music[j+1] = byte(sample), byte(uint16(sample)>>8)
		}
	}
}

func safeInstance(s string) string {
	s = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' {
			return r
		}
		return '-'
	}, s)
	if s == "" {
		return "device"
	}
	return s
}

func suffix(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	if s == "" {
		return fmt.Sprintf("%04d", 0)
	}
	return s
}

// Run uses Sendspin's streaming path for synchronized player audio when enabled by controller config.
func (e *Emulator) startSendspinPump(ctx context.Context) { go e.sendspinPump(ctx) }
