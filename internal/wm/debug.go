package wm

import (
	"path/filepath"
	"strings"
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
)

func (DebugOverlayAction) isEffect() {}
func (DebugRecordAction) isEffect()  {}

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

func parseDebug(spec ActionSpec, args []string, rest string) (Action, error) {
	if len(args) == 0 {
		return nil, badArgs(spec, "needs damage, opaque, planes, trace, profile or heap")
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
		return nil, badArgs(spec, "%q is not damage, opaque, planes, trace, profile or heap", args[0])
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

func debugPath(spec ActionSpec, path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", badArgs(spec, "the file %q is not an absolute path", path)
	}
	return filepath.Clean(path), nil
}
