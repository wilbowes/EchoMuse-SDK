// Package main runs a mock EchoMuse controller.
//
// It is the counterpart to the sidecar: a controller a device can register
// with, so the device can be developed and demonstrated without the real
// controller on the network.
//
// It is the same mock the SDK's own tests use, wrapped in a program. The
// mock is a library so that a test can drive it precisely; this is the same
// thing with a URL on the terminal and a device that connects to it.
//
// The mock listens on a random local port. The URL it prints is what the
// device is pointed at.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/mockcontroller"
)

func main() {
	mode := flag.String("mode", "approve", "how a registration is answered: approve, pending or refused")
	verbose := flag.Bool("verbose", false, "log at debug level")
	flag.Parse()

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(log)

	var answer mockcontroller.Mode
	switch *mode {
	case "approve":
		answer = mockcontroller.ModeApprove
	case "pending":
		answer = mockcontroller.ModePending
	case "refused":
		answer = mockcontroller.ModeRefused
	default:
		fmt.Fprintf(os.Stderr, "echomuse-mock-controller: --mode must be approve, pending or refused\n")
		os.Exit(2)
	}

	controller := mockcontroller.New(mockcontroller.Options{Mode: answer, Log: log})
	defer controller.Close()

	// The URL is printed on its own line, before anything else is written to
	// the standard output, so a script can read it with `head -1`.
	fmt.Println(controller.URL())
	os.Stdout.Sync()
	log.Info("the mock controller is listening", "url", controller.URL(), "mode", *mode)

	// The device is approved when it registers, unless the mock was told to
	// hold it. An approval is what turns "pending" into "connected" in the
	// device's own state.
	go func() {
		select {
		case <-controller.Registered():
			if answer == mockcontroller.ModeApprove {
				log.Info("the device registered", "device_id", controller.DeviceID())
				if err := controller.Approve(); err != nil {
					log.Warn("the device could not be approved", "error", err)
				} else {
					log.Info("the device was approved")
				}
			} else {
				log.Info("the device registered", "device_id", controller.DeviceID(), "held", *mode)
			}
		case <-time.After(30 * time.Second):
			log.Warn("no device registered within 30 seconds")
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	log.Info("the mock controller is stopping")
}
