// Command emulator is a device that behaves as the EchoMuse controller
// expects, with a window showing what it is doing.
//
// It is the SDK's proof of concept: something that joins a real controller,
// gets admitted, is issued a link credential, streams the machine's
// microphone and plays the response on the machine's speaker. It can also
// score the wake word locally and run a Sendspin player when configured.
//
// # Running it
//
//	go run ./cmd/emulator
//	go run ./cmd/emulator --controller ws://192.168.1.10:8767
//
// The window opens in the browser. The controller's dashboard is where the
// device is approved — that is the one human decision in the flow.
//
// # What it does
//
//   - finds the controller by mDNS, or uses the address given
//   - asks to be admitted, and repeats the request while its window is open
//   - serves the shell plane, so the controller issues it a link credential
//   - streams the machine's microphone when the controller asks, or scores
//     locally if its optional ONNX backend is configured
//   - plays the response on the machine's speaker
//   - draws the controller's ring state in the window
//   - provides an optional Sendspin multi-room player
//
// # The microphone
//
// Voice audio uses ALSA directly on Linux. It is the emulator's dependency,
// not the SDK's: the core SDK is pure Go, with ONNX Runtime isolated behind
// an optional Linux+cgo package.
//
// By default the emulator streams only after the controller asks for the
// microphone. With --wakeword and local model files configured, it scores
// locally and sends audio only after a wake crossing.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/wilbowes/EchoMuse-SDK/clients/go/cmd/emulator/audio"
	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/emulator"
	"github.com/wilbowes/EchoMuse-SDK/clients/go/echomuse/wakeword/ort"
)

func main() {
	var (
		controller      = flag.String("controller", "", "the controller's URL, e.g. ws://192.168.1.10:8767. Empty finds it by mDNS.")
		tlsPort         = flag.Int("controller-tls-port", 0, "the controller's encrypted port. Zero means it offers none.")
		deviceID        = flag.String("device-id", "", "this device's identifier. Empty derives one from the machine's name.")
		credsDir        = flag.String("credentials", "", "where the link credential is kept. Empty uses ~/.echomuse/emulator.")
		uiAddr          = flag.String("ui", "127.0.0.1:8771", "where the window is served.")
		audioInput      = flag.String("audio-input", "", "the microphone device. Empty uses the system default.")
		audioOutput     = flag.String("audio-output", "", "the speaker device. Empty uses the system default.")
		listDevices     = flag.Bool("list-audio-devices", false, "list the machine's microphones and speakers, then exit.")
		muteCapture     = flag.Bool("mute-capture", false, "do not send the microphone to the controller.")
		noWindow        = flag.Bool("no-window", false, "run without the window, for a headless machine.")
		logLevel        = flag.String("log-level", "info", "debug, info, warn or error.")
		wakewordEnabled = flag.Bool("wakeword", false, "enable local wakeword scoring (requires ONNX Runtime and model files).")
		ortLibrary      = flag.String("onnxruntime", "libonnxruntime.so", "ONNX Runtime shared library path.")
		modelDir        = flag.String("wakeword-model-dir", "", "directory containing melspectrogram.onnx and embedding_model.onnx.")
		classifier      = flag.String("wakeword-classifier", "", "selected wakeword classifier ONNX file.")
		modelName       = flag.String("wakeword-model-name", "", "wakeword model identifier used by controller config; defaults to classifier filename without extension.")
		wakeThreshold   = flag.Float64("wakeword-threshold", 0.5, "local wakeword threshold before controller config arrives.")
	)
	flag.Parse()

	if *listDevices {
		printAudioDevices()
		return
	}

	level := slog.LevelInfo
	switch *logLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(log)

	var localWake *emulator.LocalWakeOptions
	var inferer *ort.Inferer
	if *wakewordEnabled {
		if *modelDir == "" || *classifier == "" {
			log.Error("local wakeword needs --wakeword-model-dir and --wakeword-classifier")
			os.Exit(2)
		}
		runtime, err := ort.Open(*ortLibrary)
		if err != nil {
			log.Error("ONNX Runtime could not be loaded", "error", err)
			os.Exit(1)
		}
		classifierPath := *classifier
		if !filepath.IsAbs(classifierPath) {
			classifierPath = filepath.Join(*modelDir, classifierPath)
		}
		models := ort.Models{
			Melspec:    filepath.Join(*modelDir, "melspectrogram.onnx"),
			Embedding:  filepath.Join(*modelDir, "embedding_model.onnx"),
			Classifier: classifierPath,
		}
		inferer, err = runtime.NewInferer(models, ort.DefaultOptions())
		if err != nil {
			log.Error("wakeword models could not be loaded", "error", err)
			os.Exit(1)
		}
		name := *modelName
		if name == "" {
			name = strings.TrimSuffix(filepath.Base(classifierPath), filepath.Ext(classifierPath))
		}
		localWake = &emulator.LocalWakeOptions{Inferer: inferer, ModelName: name, Threshold: float32(*wakeThreshold)}
	}
	emu, err := emulator.New(emulator.Options{
		ControllerURL:     *controller,
		ControllerTLSPort: *tlsPort,
		DeviceID:          *deviceID,
		CredentialsDir:    *credsDir,
		Audio:             audio.NewBackend(*audioInput, *audioOutput),
		MuteCapture:       *muteCapture,
		LocalWake:         localWake,
		Log:               log,
	})
	if err != nil {
		log.Error("the emulator could not start", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if !*noWindow {
		url, err := serveWindow(ctx, emu, *uiAddr, log)
		if err != nil {
			log.Error("the window could not be served", "error", err)
			os.Exit(1)
		}
		log.Info("the window is open", "url", url)
		openBrowser(url, log)
	}

	log.Info("joining the controller", "audio", emu.AudioDescription())

	if err := emu.Run(ctx); err != nil && err != context.Canceled {
		log.Error("the emulator stopped", "error", err)
		os.Exit(1)
	}
	if inferer != nil {
		_ = inferer.Close()
	}
}

// printAudioDevices lists the machine's microphones and speakers.
//
// It is how a person finds the name to put in --audio-input and
// --audio-output. The names are the ones the machine's own mixer shows,
// so a name copied from there works.
func printAudioDevices() {
	inputs, outputs, err := audio.ListDevices()
	if err != nil {
		fmt.Fprintf(os.Stderr, "the audio devices could not be listed: %v\n", err)
		os.Exit(1)
	}

	if len(inputs) == 0 && len(outputs) == 0 {
		fmt.Fprintln(os.Stderr, "no audio devices were found")
		return
	}

	if len(inputs) > 0 {
		fmt.Println("Microphones:")
		printDevices(inputs)
	}
	if len(outputs) > 0 {
		if len(inputs) > 0 {
			fmt.Println()
		}
		fmt.Println("Speakers:")
		printDevices(outputs)
	}
}

// printDevices writes a list of devices, one per line.
//
// The system default is marked, because it is the one most machines want
// and the one the emulator takes when nothing is chosen.
func printDevices(devices []emulator.AudioDevice) {
	for _, d := range devices {
		marker := ""
		if d.Default {
			marker = "  (default)"
		}
		if d.Description != "" {
			fmt.Printf("  %-64s %s%s\n", d.Name, d.Description, marker)
			continue
		}
		fmt.Printf("  %-64s%s\n", d.Name, marker)
	}
}

// serveWindow starts the window's server and returns its URL.
func serveWindow(ctx context.Context, emu *emulator.Emulator, addr string, log *slog.Logger) (string, error) {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return "", fmt.Errorf("listening on %s: %w", addr, err)
	}
	srv := &http.Server{
		Handler:           emulator.NewHandler(emu),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()

	go func() {
		if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Error("the window's server stopped", "error", err)
		}
	}()

	host, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		return "", fmt.Errorf("reading the window listener address: %w", err)
	}
	if host == "" || host == "::" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port), nil
}

// openBrowser opens the window in the machine's browser.
//
// It is best effort: a machine with no browser is a machine where the
// emulator runs headless, and the URL is logged either way.
func openBrowser(url string, log *slog.Logger) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}

	if err := cmd.Start(); err != nil {
		log.Info("open the window yourself", "url", url)
	}
}
