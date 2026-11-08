package ipc

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"slices"
	"time"

	"github.com/stubbedev/lys-lib/wmipc"
)

const captureBacklog = 4096

type BarrierID uint32

type Barrier struct {
	ID BarrierID `json:"id"`
	X1 int       `json:"x1"`
	Y1 int       `json:"y1"`
	X2 int       `json:"x2"`
	Y2 int       `json:"y2"`
}

func (b Barrier) Vertical() bool {
	return b.X1 == b.X2
}

func (b Barrier) check() error {
	switch {
	case b.ID == 0:
		return fmt.Errorf("%w: barrier %+v has id 0", ErrBadRequest, b)
	case b.X1 == b.X2 && b.Y1 == b.Y2:
		return fmt.Errorf("%w: barrier %d is a single point, neither horizontal nor vertical", ErrBadRequest, b.ID)
	case b.X1 != b.X2 && b.Y1 != b.Y2:
		return fmt.Errorf("%w: barrier %d from %d,%d to %d,%d is diagonal", ErrBadRequest, b.ID, b.X1, b.Y1, b.X2, b.Y2)
	}
	return nil
}

func (b Barrier) ordered() Barrier {
	b.X1, b.X2 = min(b.X1, b.X2), max(b.X1, b.X2)
	b.Y1, b.Y2 = min(b.Y1, b.Y2), max(b.Y1, b.Y2)
	return b
}

type CaptureCommand interface {
	captureCommand()
}

type (
	SetBarriers    struct{ Barriers []Barrier }
	EnableCapture  struct{}
	DisableCapture struct{}
	ReleaseCapture struct{ At *wmipc.CapturePoint }
)

func (SetBarriers) captureCommand()    {}
func (EnableCapture) captureCommand()  {}
func (DisableCapture) captureCommand() {}
func (ReleaseCapture) captureCommand() {}

type captureLine struct {
	Command  string              `json:"command"`
	Barriers *[]Barrier          `json:"barriers,omitempty"`
	At       *wmipc.CapturePoint `json:"at,omitempty"`
}

const (
	commandBarriers = "barriers"
	commandEnable   = "enable"
	commandDisable  = "disable"
	commandRelease  = "release"
)

func EncodeCaptureCommand(cmd CaptureCommand) ([]byte, error) {
	var l captureLine
	switch c := cmd.(type) {
	case SetBarriers:
		barriers := nonNil(c.Barriers)
		l = captureLine{Command: commandBarriers, Barriers: &barriers}
	case EnableCapture:
		l = captureLine{Command: commandEnable}
	case DisableCapture:
		l = captureLine{Command: commandDisable}
	case ReleaseCapture:
		l = captureLine{Command: commandRelease, At: c.At}
	default:
		return nil, fmt.Errorf("%w: unknown capture command %T", ErrBadRequest, cmd)
	}
	line, err := json.Marshal(l)
	if err != nil {
		return nil, fmt.Errorf("ipc: encode the %s capture command: %w", l.Command, err)
	}
	return append(line, '\n'), nil
}

func parseCaptureCommand(line []byte) (CaptureCommand, error) {
	var raw struct {
		Command  string              `json:"command"`
		Barriers *[]Barrier          `json:"barriers"`
		At       *wmipc.CapturePoint `json:"at"`
	}
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBadRequest, err)
	}
	if (raw.Command == commandBarriers) != (raw.Barriers != nil) {
		return nil, fmt.Errorf("%w: only the barriers command takes barriers, and it needs them", ErrBadRequest)
	}
	if raw.Command != commandRelease && raw.At != nil {
		return nil, fmt.Errorf("%w: only the release command takes a point", ErrBadRequest)
	}
	switch raw.Command {
	case commandBarriers:
		return parseBarriers(*raw.Barriers)
	case commandEnable:
		return EnableCapture{}, nil
	case commandDisable:
		return DisableCapture{}, nil
	case commandRelease:
		if raw.At != nil && !finite(*raw.At) {
			return nil, fmt.Errorf("%w: release at %v, not a finite point", ErrBadRequest, *raw.At)
		}
		return ReleaseCapture{At: raw.At}, nil
	}
	return nil, fmt.Errorf("%w: unknown capture command %q (known: %s, %s, %s, %s)", ErrBadRequest, raw.Command, commandBarriers, commandEnable, commandDisable, commandRelease)
}

func finite(p wmipc.CapturePoint) bool {
	return !math.IsNaN(p.X) && !math.IsInf(p.X, 0) && !math.IsNaN(p.Y) && !math.IsInf(p.Y, 0)
}

func parseBarriers(in []Barrier) (SetBarriers, error) {
	out := make([]Barrier, 0, len(in))
	for _, b := range in {
		if err := b.check(); err != nil {
			return SetBarriers{}, err
		}
		if slices.ContainsFunc(out, func(o Barrier) bool { return o.ID == b.ID }) {
			return SetBarriers{}, fmt.Errorf("%w: barrier id %d given twice", ErrBadRequest, b.ID)
		}
		out = append(out, b.ordered())
	}
	return SetBarriers{Barriers: out}, nil
}

type (
	CaptureKeymapEvent struct {
		Keymap string `json:"keymap"`
	}
	CaptureActivatedEvent struct {
		Activation uint32             `json:"activation"`
		At         wmipc.CapturePoint `json:"at"`
		Barrier    BarrierID          `json:"barrier"`
	}
	CaptureDeactivatedEvent struct {
		Activation uint32             `json:"activation"`
		At         wmipc.CapturePoint `json:"at"`
	}
	CaptureDisabledEvent struct{}
	CaptureMotionEvent   struct {
		DX float64 `json:"dx"`
		DY float64 `json:"dy"`
	}
	CaptureButtonEvent struct {
		Button  uint32 `json:"button"`
		Pressed bool   `json:"pressed"`
	}
	CaptureScrollEvent struct {
		DX float64 `json:"dx"`
		DY float64 `json:"dy"`
	}
	CaptureScrollDiscreteEvent struct {
		DX int32 `json:"dx"`
		DY int32 `json:"dy"`
	}
	CaptureScrollStopEvent struct {
		X bool `json:"x"`
		Y bool `json:"y"`
	}
	CaptureFrameEvent struct{}
	CaptureKeyEvent   struct {
		Key     uint32 `json:"key"`
		Pressed bool   `json:"pressed"`
	}
	CaptureModifiersEvent struct {
		Depressed uint32 `json:"depressed"`
		Latched   uint32 `json:"latched"`
		Locked    uint32 `json:"locked"`
		Group     uint32 `json:"group"`
	}
	CaptureAppliedEvent struct{}
	CaptureErrorEvent   struct {
		Error string `json:"error"`
	}
)

func (CaptureKeymapEvent) Name() string         { return string(wmipc.CaptureKeymap) }
func (CaptureActivatedEvent) Name() string      { return string(wmipc.CaptureActivated) }
func (CaptureDeactivatedEvent) Name() string    { return string(wmipc.CaptureDeactivated) }
func (CaptureDisabledEvent) Name() string       { return string(wmipc.CaptureDisabled) }
func (CaptureMotionEvent) Name() string         { return string(wmipc.CaptureMotion) }
func (CaptureButtonEvent) Name() string         { return string(wmipc.CaptureButton) }
func (CaptureScrollEvent) Name() string         { return string(wmipc.CaptureScroll) }
func (CaptureScrollDiscreteEvent) Name() string { return string(wmipc.CaptureScrollDiscrete) }
func (CaptureScrollStopEvent) Name() string     { return string(wmipc.CaptureScrollStop) }
func (CaptureFrameEvent) Name() string          { return string(wmipc.CaptureFrame) }
func (CaptureKeyEvent) Name() string            { return string(wmipc.CaptureKey) }
func (CaptureModifiersEvent) Name() string      { return string(wmipc.CaptureModifiers) }
func (CaptureErrorEvent) Name() string          { return string(wmipc.CaptureError) }
func (CaptureAppliedEvent) Name() string        { return string(wmipc.CaptureApplied) }

type CaptureSession interface {
	Apply(cmd CaptureCommand) error
	Close() error
}

type captureStream struct {
	lines chan []byte
	full  bool
}

func (s *captureStream) send(report func(error), e Event) {
	if s.full {
		return
	}
	line, err := Encode(e)
	if err != nil {
		report(err)
		return
	}
	select {
	case s.lines <- line:
	default:
		s.full = true
		close(s.lines)
	}
}

func (s *Server) capture(conn *net.UnixConn, r *bufio.Reader) {
	stream := &captureStream{lines: make(chan []byte, captureBacklog)}
	session, err := onLoop(s, func() (CaptureSession, error) {
		return s.handler.Capture(func(e Event) { stream.send(s.report, e) })
	})
	if err != nil {
		s.reply(conn, nil, err)
		return
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		s.endCapture(stream, session)
		s.reply(conn, nil, fmt.Errorf("ipc: keep the capture connection: %w", err))
		return
	}
	s.reply(conn, nil, nil)
	written := make(chan struct{})
	go func() {
		defer close(written)
		defer func() { _ = conn.CloseRead() }()
		for line := range stream.lines {
			if write(conn, line) != nil {
				return
			}
		}
	}()
	if err := s.readCapture(r, stream, session); err != nil {
		s.post(func() { stream.send(s.report, CaptureErrorEvent{Error: err.Error()}) })
	}
	s.endCapture(stream, session)
	select {
	case <-written:
	case <-s.done:
	}
}

func (s *Server) readCapture(r *bufio.Reader, stream *captureStream, session CaptureSession) error {
	for {
		line, err := r.ReadSlice('\n')
		switch {
		case errors.Is(err, bufio.ErrBufferFull):
			return fmt.Errorf("%w: a capture command longer than %d bytes", ErrBadRequest, maxRequest)
		case errors.Is(err, io.EOF), errors.Is(err, net.ErrClosed):
			return nil
		case err != nil:
			return fmt.Errorf("ipc: read a capture command: %w", err)
		}
		cmd, err := parseCaptureCommand(line)
		if err != nil {
			return err
		}
		if _, err := onLoop(s, func() (struct{}, error) { return struct{}{}, s.applyCapture(stream, session, cmd) }); err != nil {
			if errors.Is(err, ErrShutdown) {
				return nil
			}
			return err
		}
	}
}

func (s *Server) endCapture(stream *captureStream, session CaptureSession) {
	s.post(func() {
		if err := session.Close(); err != nil {
			s.report(fmt.Errorf("ipc: end an input capture: %w", err))
		}
		if !stream.full {
			stream.full = true
			close(stream.lines)
		}
	})
}

func (s *Server) applyCapture(stream *captureStream, session CaptureSession, cmd CaptureCommand) error {
	if err := session.Apply(cmd); err != nil {
		return err
	}
	stream.send(s.report, CaptureAppliedEvent{})
	return nil
}
