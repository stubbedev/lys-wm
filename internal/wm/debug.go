package wm

import (
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type DebugOverlay uint8

const (
	DebugDamage DebugOverlay = iota + 1
	DebugOpaque
	DebugPlanes
)

var debugOverlays = []string{DebugDamage: "damage", DebugOpaque: "opaque", DebugPlanes: "planes"}

func (d DebugOverlay) String() string {
	return enumName(debugOverlays, d, "DebugOverlay")
}

type DebugRecording uint8

const (
	DebugTrace DebugRecording = iota + 1
	DebugProfile
	DebugHeap
)

var debugRecordings = []string{DebugTrace: "trace", DebugProfile: "profile", DebugHeap: "heap"}

func (d DebugRecording) String() string {
	return enumName(debugRecordings, d, "DebugRecording")
}

type (
	DebugOverlayAction struct {
		Overlay DebugOverlay
		Mode    Switch
	}
	DebugRecordAction struct {
		Recording DebugRecording
		Stop      bool
		Path      string
	}
	DebugProtocolAction struct {
		Mode Switch
	}
	DebugProtocolDumpAction struct {
		Path string
	}
	DebugSpinAction struct {
		For time.Duration
	}
)

const debugSpinMostMillis = 10000

func (DebugOverlayAction) isEffect()      {}
func (DebugRecordAction) isEffect()       {}
func (DebugProtocolAction) isEffect()     {}
func (DebugProtocolDumpAction) isEffect() {}
func (DebugSpinAction) isEffect()         {}

const debugTargets = "damage, opaque, planes, protocol, spin, trace, profile or heap"

func (a DebugOverlayAction) String() string {
	return "debug " + a.Overlay.String() + " " + a.Mode.String()
}

func (a DebugOverlayAction) apply(s State, _ Settings) (State, []Effect, error) {
	return emit(s, a)
}

func (a DebugRecordAction) String() string {
	switch {
	case a.Recording == DebugHeap:
		return "debug heap " + a.Path
	case a.Stop:
		return "debug " + a.Recording.String() + " stop"
	}
	return "debug " + a.Recording.String() + " start " + a.Path
}

func (a DebugRecordAction) apply(s State, _ Settings) (State, []Effect, error) {
	return emit(s, a)
}

func (a DebugProtocolAction) String() string {
	return "debug protocol " + a.Mode.String()
}

func (a DebugProtocolAction) apply(s State, _ Settings) (State, []Effect, error) {
	return emit(s, a)
}

func (a DebugProtocolDumpAction) String() string {
	return "debug protocol dump " + a.Path
}

func (a DebugProtocolDumpAction) apply(s State, _ Settings) (State, []Effect, error) {
	return emit(s, a)
}

func (a DebugSpinAction) String() string {
	return "debug spin " + strconv.FormatInt(a.For.Milliseconds(), 10)
}

func (a DebugSpinAction) apply(s State, _ Settings) (State, []Effect, error) {
	return emit(s, a)
}

func parseDebugSpin(spec ActionSpec, args []string) (Action, error) {
	if len(args) != 1 {
		return nil, badArgs(spec, "spin takes the milliseconds to spin and nothing more, not %q", strings.Join(args, " "))
	}
	ms, err := strconv.ParseUint(args[0], 10, 16)
	if err != nil || ms == 0 || ms > debugSpinMostMillis {
		return nil, badArgs(spec, "spin's %q is not milliseconds from 1 to %d", args[0], debugSpinMostMillis)
	}
	return DebugSpinAction{For: time.Duration(ms) * time.Millisecond}, nil
}

func parseDebug(spec ActionSpec, args []string, rest string) (Action, error) {
	if len(args) == 0 {
		return nil, badArgs(spec, "needs "+debugTargets)
	}
	if args[0] == "spin" {
		return parseDebugSpin(spec, args[1:])
	}
	if args[0] == "protocol" {
		return parseDebugProtocol(spec, args[1:], strings.TrimSpace(strings.TrimPrefix(rest, args[0])))
	}
	if o, ok := parseEnum[DebugOverlay](debugOverlays, args[0]); ok {
		mode, err := optionalSwitch(spec, args[1:])
		if err != nil {
			return nil, err
		}
		return DebugOverlayAction{Overlay: o, Mode: mode}, nil
	}
	r, ok := parseEnum[DebugRecording](debugRecordings, args[0])
	if !ok {
		return nil, badArgs(spec, "%q is not "+debugTargets, args[0])
	}
	rest = strings.TrimSpace(strings.TrimPrefix(rest, args[0]))
	if r == DebugHeap {
		path, err := debugPath(spec, rest)
		return DebugRecordAction{Recording: r, Path: path}, err
	}
	verb, file, _ := strings.Cut(rest, " ")
	switch verb {
	case "stop":
		if file != "" {
			return nil, badArgs(spec, "%s stop takes nothing more, not %q", r, file)
		}
		return DebugRecordAction{Recording: r, Stop: true}, nil
	case "start":
		path, err := debugPath(spec, strings.TrimSpace(file))
		return DebugRecordAction{Recording: r, Path: path}, err
	}
	return nil, badArgs(spec, "%s takes start <file> or stop, not %q", r, rest)
}

func parseDebugProtocol(spec ActionSpec, args []string, rest string) (Action, error) {
	if len(args) > 0 && args[0] == "dump" {
		path, err := debugPath(spec, strings.TrimSpace(strings.TrimPrefix(rest, args[0])))
		return DebugProtocolDumpAction{Path: path}, err
	}
	mode, err := optionalSwitch(spec, args)
	if err != nil {
		return nil, err
	}
	return DebugProtocolAction{Mode: mode}, nil
}

func debugPath(spec ActionSpec, path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", badArgs(spec, "the file %q is not an absolute path", path)
	}
	return filepath.Clean(path), nil
}
