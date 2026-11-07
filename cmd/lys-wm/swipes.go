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
		dx, dy, err := deltas(verb, arg)
		if err != nil {
			return err
		}
		return p.Swipe(dx, dy)
	case "move":
		dx, dy, err := deltas(verb, arg)
		if err != nil {
			return err
		}
		return errors.Join(p.Move(dx, dy), p.Frame())
	}
	return fmt.Errorf("unknown swipe line %q (known: begin, swipe <dx> <dy>, end, move <dx> <dy>)", line)
}

func deltas(verb, arg string) (dx, dy float64, err error) {
	x, y, _ := strings.Cut(arg, " ")
	if dx, err = strconv.ParseFloat(x, 64); err != nil {
		return 0, 0, fmt.Errorf("%s %q: dx: %w", verb, arg, err)
	}
	if dy, err = strconv.ParseFloat(y, 64); err != nil {
		return 0, 0, fmt.Errorf("%s %q: dy: %w", verb, arg, err)
	}
	return dx, dy, nil
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
			err := applySwipeLine(h.Pointer(), string(partial[:end]))
			partial = append(partial[:0], partial[end+1:]...)
			if err != nil {
				return err
			}
		}
	})
	return err
}
