package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/loop"
)

const (
	swipeFingers   = 3
	swipeReadBytes = 4096
)

func applySwipeLine(p *headless.Pointer, line string) error {
	verb, arg, _ := strings.Cut(strings.TrimSpace(line), " ")
	switch verb {
	case "begin":
		return p.GestureBegin(backend.Swipe, swipeFingers)
	case "end":
		return p.GestureEnd(backend.Swipe, false)
	case "swipe":
		dx, err := strconv.ParseFloat(arg, 64)
		if err != nil {
			return fmt.Errorf("swipe %q: %w", arg, err)
		}
		return p.Swipe(dx, 0)
	}
	return fmt.Errorf("unknown swipe line %q (known: begin, swipe <dx>, end)", line)
}

func readSwipes(l *loop.Loop, b backend.Backend, stdin *os.File) error {
	h, ok := b.(*headless.Backend)
	if !ok {
		return errors.New("-headless-swipes needs the headless backend, whose touchpad is a fake")
	}
	chunk := make([]byte, swipeReadBytes)
	var partial []byte
	var source *loop.Source
	var err error
	source, err = l.AddFD(int(stdin.Fd()), loop.Readable, func(loop.Events) error {
		n, err := stdin.Read(chunk)
		if errors.Is(err, io.EOF) {
			return source.Remove()
		}
		if err != nil {
			return fmt.Errorf("read a swipe: %w", err)
		}
		partial = append(partial, chunk[:n]...)
		for {
			end := bytes.IndexByte(partial, '\n')
			if end < 0 {
				return nil
			}
			line := string(partial[:end])
			partial = partial[end+1:]
			if err := applySwipeLine(h.Pointer(), line); err != nil {
				return err
			}
		}
	})
	return err
}
