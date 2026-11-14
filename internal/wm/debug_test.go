package wm

import (
	"errors"
	"strings"
	"testing"
	"time"
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
		DebugProtocolAction{Mode: SwitchOn},
		DebugProtocolAction{Mode: SwitchOff},
		DebugProtocolDumpAction{Path: "/tmp/a wire.log"},
		DebugSpinAction{For: 500 * time.Millisecond},
		DebugSpinAction{For: 10 * time.Second},
	} {
		got, err := ParseAction(a.String())
		if err != nil || got != a {
			t.Errorf("%q parsed to %#v, %v", a.String(), got, err)
		}
	}
	if got, err := ParseAction("debug damage"); err != nil || got != (DebugOverlayAction{Overlay: DebugDamage, Mode: SwitchToggle}) {
		t.Errorf("debug damage parsed to %#v, %v", got, err)
	}
	if got, err := ParseAction("debug protocol"); err != nil || got != (DebugProtocolAction{Mode: SwitchToggle}) {
		t.Errorf("debug protocol parsed to %#v, %v", got, err)
	}
	for text, want := range map[string]string{
		"debug":                     "needs damage, opaque, planes, protocol",
		"debug colours":             `"colours" is not damage`,
		"debug damage sideways":     `"sideways" is not toggle, on or off`,
		"debug trace start":         `the file "" is not an absolute path`,
		"debug trace start a.jsonl": `the file "a.jsonl" is not an absolute path`,
		"debug trace stop now":      `trace stop takes nothing more`,
		"debug profile pause":       `profile takes start <file> or stop`,
		"debug heap":                `the file "" is not an absolute path`,
		"debug protocol dump":       `the file "" is not an absolute path`,
		"debug protocol dump a.log": `the file "a.log" is not an absolute path`,
		"debug protocol loud":       `"loud" is not toggle, on or off`,
		"debug spin":                `spin takes the milliseconds to spin and nothing more, not ""`,
		"debug spin 5 ms":           `spin takes the milliseconds to spin and nothing more, not "5 ms"`,
		"debug spin 0":              `spin's "0" is not milliseconds from 1 to 10000`,
		"debug spin 10001":          `spin's "10001" is not milliseconds from 1 to 10000`,
		"debug spin -3":             `spin's "-3" is not milliseconds from 1 to 10000`,
		"debug spin soon":           `spin's "soon" is not milliseconds from 1 to 10000`,
	} {
		if _, err := ParseAction(text); !errors.Is(err, ErrBadAction) || !strings.Contains(err.Error(), want) {
			t.Errorf("ParseAction(%q) = %v, want %q", text, err, want)
		}
	}
}

func TestCastTargetRefusesBadArguments(t *testing.T) {
	for text, want := range map[string]string{
		"casttarget":                 "takes window, output or clear",
		"casttarget screen":          `"screen" is not window, output or clear`,
		"casttarget clear now":       `clear takes nothing more`,
		"casttarget window zero":     `window ID "zero" is not a positive number`,
		"casttarget output DP-1 HDR": "at most one more argument",
	} {
		if _, err := ParseAction(text); !errors.Is(err, ErrBadAction) || !strings.Contains(err.Error(), want) {
			t.Errorf("ParseAction(%q) = %v, want %q", text, err, want)
		}
	}
}
