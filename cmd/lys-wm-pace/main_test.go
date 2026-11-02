package main

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/frametrace"
	"github.com/stubbedev/lys-wm/internal/testenv"
)

func TestOptionsAreCheckedAtTheBoundary(t *testing.T) {
	for _, args := range [][]string{{"-backend", "nested"}, {"-renderer", "metal"}, {"-rounds", "0"}, {"-scenarios", "nope"}} {
		if _, err := parseOptions(args, io.Discard); err == nil {
			t.Errorf("%v was accepted", args)
		}
	}
	o, err := parseOptions([]string{"-scenarios", "move,genie"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if o.backend != "headless" || len(o.only) != 2 {
		t.Fatalf("options = %+v", o)
	}
}

func TestDRMRefusesARunningSession(t *testing.T) {
	for _, env := range []map[string]string{{"WAYLAND_DISPLAY": "wayland-1"}, {"DISPLAY": ":0"}, {"XDG_SESSION_TYPE": "wayland"}, {"XDG_SESSION_TYPE": "x11"}} {
		if err := refuseLiveSession(func(name string) string { return env[name] }); err == nil {
			t.Errorf("%v was not refused", env)
		}
	}
	if err := refuseLiveSession(func(name string) string { return map[string]string{"XDG_SESSION_TYPE": "tty"}[name] }); err != nil {
		t.Errorf("a text console was refused: %v", err)
	}
}

func TestASoftwareRasteriserIsNotAGPU(t *testing.T) {
	soft := frametrace.Header{Backend: "headless", Renderer: "vulkan", Device: "llvmpipe (LLVM 21)", Software: true}
	err := softwareRefusal(options{}, soft)
	if err == nil || !strings.Contains(err.Error(), "no real GPU") || !strings.Contains(err.Error(), "llvmpipe") {
		t.Fatalf("refusal = %v", err)
	}
	if err := softwareRefusal(options{allowSoftware: true}, soft); err != nil {
		t.Fatal(err)
	}
	if err := softwareRefusal(options{}, frametrace.Header{Device: "Mesa Intel(R) Graphics (LNL)"}); err != nil {
		t.Fatal(err)
	}
}

func TestTicksKeepTheirCadenceWhateverEachOneCosts(t *testing.T) {
	now := time.Second
	var at []time.Duration
	onTicks(4, 8*time.Millisecond, func() time.Duration { return now }, func(d time.Duration) { now += max(d, 0) }, func() {
		at = append(at, now)
		now += 3 * time.Millisecond
	})
	want := []time.Duration{time.Second, time.Second + 8*time.Millisecond, time.Second + 16*time.Millisecond, time.Second + 24*time.Millisecond}
	if !slices.Equal(at, want) {
		t.Fatalf("ticks at %v, want %v", at, want)
	}
}

func TestStepsAreSummarizedPerRun(t *testing.T) {
	frame := func(start time.Duration) frametrace.Frame {
		return frametrace.Frame{Output: "A", Kind: frametrace.Composited, Start: start, Present: start + 10, Refresh: 10, Animating: true}
	}
	marks := []mark{{step: "a", from: 0, to: 100}, {step: "b", from: 100, to: 200}, {step: "a", from: 1000, to: 1100}}
	r := buildReport(frametrace.Header{Device: "x"}, 2, marks, []frametrace.Frame{frame(10), frame(20), frame(110), frame(150), frame(1010), frame(5000)})
	if len(r.Steps) != 2 || r.Steps[0].Name != "a" || len(r.Steps[0].Runs) != 2 {
		t.Fatalf("report = %+v", r)
	}
	if s := r.Steps[0].Summary; s.Frames != 3 || s.Interval.Samples != 1 {
		t.Fatalf("step a = %+v", s)
	}
}

func TestTheGateNamesWhatIsLate(t *testing.T) {
	r := report{Steps: []stepReport{{Name: "smooth"}, {Name: "rough", Summary: frametrace.Summary{LatePercent: 12}}, {Name: "reported", Ungated: true, Summary: frametrace.Summary{LatePercent: 90}}}}
	if err := r.gate(-1); err != nil {
		t.Fatal(err)
	}
	if err := r.gate(20); err != nil {
		t.Fatal(err)
	}
	err := r.gate(5)
	if err == nil || !strings.Contains(err.Error(), "rough") || strings.Contains(err.Error(), "smooth") || strings.Contains(err.Error(), "reported") {
		t.Fatalf("gate = %v", err)
	}
}

func buildCompositor(t *testing.T) string {
	t.Helper()
	testenv.Keymap(t)
	goTool := testenv.Require(t, "go")
	binary := filepath.Join(t.TempDir(), "lys-wm")
	out, err := exec.Command(goTool, "build", "-o", binary, "../lys-wm").CombinedOutput()
	if err != nil {
		t.Fatalf("build lys-wm: %v\n%s", err, out)
	}
	return binary
}

func TestMeasuringASoftwareRendererEndToEnd(t *testing.T) {
	binary := buildCompositor(t)
	jsonPath := filepath.Join(t.TempDir(), "report.json")
	refused := measure(options{backend: "headless", renderer: "soft", compositor: binary, rounds: 1, only: []string{"glass-still"}, maxLatePercent: -1}, io.Discard, os.Getenv)
	if refused == nil || !strings.Contains(refused.Error(), "no real GPU") {
		t.Fatalf("the software renderer passed for a GPU: %v", refused)
	}
	var text strings.Builder
	o := options{backend: "headless", renderer: "soft", compositor: binary, rounds: 1, only: []string{"window", "swipe", "glass-still", "overview-scrub-10"}, maxLatePercent: -1, allowSoftware: true, jsonPath: jsonPath}
	if err := measure(o, &text, os.Getenv); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text.String(), "SOFTWARE RASTERISER") || !strings.Contains(text.String(), "simulated vsync") || !strings.Contains(text.String(), "window-open") {
		t.Fatalf("report:\n%s", text.String())
	}
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatal(err)
	}
	var r report
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatal(err)
	}
	if r.RealGPU || len(r.Steps) != 11 {
		t.Fatalf("report = %+v", r)
	}
	if s := r.Steps[0].Summary; r.Steps[0].Name != "window-open" || s.Presented == 0 || s.CPU.Samples == 0 {
		t.Fatalf("window-open = %+v", s)
	}
	still := r.Steps[6]
	if still.Name != "glass-over-still" || still.Summary.Presented != 0 {
		t.Fatalf("glass over nothing moving redrew: %+v", still.Summary)
	}
}

func TestAMissingCompositorFailsWithItsLog(t *testing.T) {
	script := filepath.Join(t.TempDir(), "lys-wm")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'no render node for you' >&2\nexit 3\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	err := measure(options{backend: "headless", renderer: "vulkan", compositor: script, rounds: 1, maxLatePercent: -1}, io.Discard, os.Getenv)
	if err == nil || !strings.Contains(err.Error(), "did not start") || !strings.Contains(err.Error(), "no render node for you") || !strings.Contains(err.Error(), "/dev/dri") {
		t.Fatalf("error = %v", err)
	}
}
