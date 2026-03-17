package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"runtime/pprof"
	"syscall"

	"github.com/msstnk/vxmon/internal/app"
	"github.com/msstnk/vxmon/internal/debuglog"
	"github.com/msstnk/vxmon/internal/store"

	tea "github.com/charmbracelet/bubbletea"
)

// vxmon.go wires store events into the Bubble Tea program.
// main is the process entrypoint and is invoked by `go run`/the built binary.
func main() {
	debugFile, err := debuglog.ConfigureFromEnv("debug.log")
	if err != nil {
		fmt.Println("fatal:", err)
		os.Exit(1)
	}
	if debugFile != nil {
		defer debugFile.Close()
	}

	debuglog.Infof("vxmon start")
	stopCPUProfile, err := startCPUProfileFromEnv()
	if err != nil {
		log.Fatal(err)
	}
	defer stopCPUProfile()
	defer debuglog.Infof("vxmon stop")
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	st := store.New()
	m := app.NewModel(st)

	p := tea.NewProgram(&m, tea.WithAltScreen())

	go store.ListenNetlink(ctx, st.SelfNamespaceID(), func(msg any) {
		p.Send(msg)
	})

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error running program: %v", err)
		os.Exit(1)
	}
}

func startCPUProfileFromEnv() (func(), error) {
	path := os.Getenv("VXMON_CPU_PROFILE")
	if path == "" {
		return func() {}, nil
	}
	if path == "1" {
		path = "cpu.pprof"
	}

	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	if err := pprof.StartCPUProfile(f); err != nil {
		f.Close()
		return nil, err
	}
	debuglog.Infof("cpu profiling enabled path=%s", path)

	return func() {
		pprof.StopCPUProfile()
		_ = f.Close()
	}, nil
}
