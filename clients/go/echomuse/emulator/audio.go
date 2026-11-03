package emulator

import "github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse"

// Audio interfaces are re-exported here for compatibility with emulator
// applications. Their system-neutral definitions live in the SDK package.
type AudioBackend = echomuse.AudioBackend
type PlaybackDrainer = echomuse.PlaybackDrainer
type FailureReporter = echomuse.AudioFailureReporter
type AudioDevice = echomuse.AudioDevice
type AudioDeviceProvider = echomuse.AudioDeviceProvider
type ClockedSendspinOutput = echomuse.ClockedAudioOutput
type SendspinOutputBackend = echomuse.ClockedAudioBackend
