//go:build !linux || !cgo

package emulator

func defaultSendspinOutputBackend() SendspinOutputBackend { return nil }
