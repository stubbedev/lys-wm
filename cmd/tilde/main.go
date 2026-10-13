// Command tilde is a pure-Go tiling Wayland compositor.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"

	"github.com/stubbedev/tilde/internal/loop"
	"github.com/stubbedev/tilde/internal/server"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "tilde:", err)
		os.Exit(1)
	}
}

type debug struct {
	wire bool
}

func parseDebug(s string) (debug, error) {
	var d debug
	for category := range strings.SplitSeq(s, ",") {
		switch strings.TrimSpace(category) {
		case "":
		case "wire":
			d.wire = true
		default:
			return debug{}, fmt.Errorf("TILDE_DEBUG: unknown category %q (known: wire)", category)
		}
	}
	return d, nil
}

func run() (err error) {
	dbg, err := parseDebug(os.Getenv("TILDE_DEBUG"))
	if err != nil {
		return err
	}
	var trace io.Writer
	if dbg.wire {
		trace = os.Stderr
	}
	l, err := loop.New()
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, l.Close()) }()
	display, err := server.New(l, server.Options{RuntimeDir: os.Getenv("XDG_RUNTIME_DIR"), Trace: trace})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, display.Close()) }()
	if err := os.Setenv("WAYLAND_DISPLAY", display.Name()); err != nil {
		return fmt.Errorf("export WAYLAND_DISPLAY: %w", err)
	}
	if _, err := l.AddSignals(func(os.Signal) error {
		l.Stop()
		return nil
	}, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "tilde: listening on %s\n", display.Name())
	return l.Run()
}
