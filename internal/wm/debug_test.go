package wm

import (
	"errors"
	"strings"
	"testing"
)

func TestDebugActionsRoundTripAndRefuseBadArguments(t *testing.T) {
	for _, a := range []Action{
		DebugOverlayAction{Overlay: DebugDamage, Mode: SwitchOn},
		DebugOverlayAction{Overlay: DebugOpaque, Mode: SwitchToggle},
		DebugOverlayAction{Overlay: DebugPlanes, Mode: SwitchOff},
		DebugRecordAction{Recording: DebugTrace, Path: "/tmp/a trace.jsonl"},
		DebugRecordAction{Recording: DebugTrace, Stop: true},
		DebugRecordAction{Recording: DebugProfile, Path: "/tmp/cpu.pprof"},
		DebugRecordAction{Recording: DebugProfile, Stop: true},
		DebugRecordAction{Recording: DebugHeap, Path: "/tmp/heap.pprof"},
	} {
		got, err := ParseAction(a.String())
		if err != nil || got != a {
			t.Errorf("%q parsed to %#v, %v", a.String(), got, err)
		}
	}
	if got, err := ParseAction("debug damage"); err != nil || got != (DebugOverlayAction{Overlay: DebugDamage, Mode: SwitchToggle}) {
		t.Errorf("debug damage parsed to %#v, %v", got, err)
	}
	for text, want := range map[string]string{
		"debug":                     "needs damage, opaque, planes",
		"debug colours":             `"colours" is not damage`,
		"debug damage sideways":     `"sideways" is not toggle, on or off`,
		"debug trace start":         `the file "" is not an absolute path`,
		"debug trace start a.jsonl": `the file "a.jsonl" is not an absolute path`,
		"debug trace stop now":      `trace stop takes nothing more`,
		"debug profile pause":       `profile takes start <file> or stop`,
		"debug heap":                `the file "" is not an absolute path`,
	} {
		if _, err := ParseAction(text); !errors.Is(err, ErrBadAction) || !strings.Contains(err.Error(), want) {
			t.Errorf("ParseAction(%q) = %v, want %q", text, err, want)
		}
	}
}
