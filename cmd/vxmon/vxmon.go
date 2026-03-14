package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"vxmon/internal/app"
	"vxmon/internal/store"

	tea "github.com/charmbracelet/bubbletea"
)

// vxmon.go wires store events into the Bubble Tea program.
// main is the process entrypoint and is invoked by `go run`/the built binary.
func main() {
	if len(os.Getenv("VXMON_DEBUG")) > 0 {
		f, err := tea.LogToFile("debug.log", "debug")
		if err != nil {
			fmt.Println("fatal:", err)
			os.Exit(1)
		}
		defer f.Close()
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	st := store.New()
	m := app.NewModel(st)

	p := tea.NewProgram(m, tea.WithAltScreen())

	go store.ListenNetlink(ctx, func(msg any) {
		p.Send(msg)
	})

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error running program: %v", err)
		os.Exit(1)
	}
}
