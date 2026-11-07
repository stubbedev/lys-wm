package main

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
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
	r := buildReport(frametrace.Header{Device: "x"}, 2, marks, nil, []frametrace.Frame{frame(10), frame(20), frame(110), frame(150), frame(1010), frame(5000)})
	if len(r.Steps) != 2 || r.Steps[0].Name != "a" || len(r.Steps[0].Runs) != 2 {
		t.Fatalf("report = %+v", r)
	}
	if s := r.Steps[0].Summary; s.Frames != 3 || s.Interval.Samples != 1 {
		t.Fatalf("step a = %+v", s)
	}
}

func TestOverviewStepsReportHowLongTheMotionTakes(t *testing.T) {
	frame := func(start time.Duration, p float64) frametrace.Frame {
		return frametrace.Frame{Output: "A", Kind: frametrace.Composited, Start: start, Present: start + 10, Refresh: 10, Animating: true, Cost: frametrace.Cost{Overview: p}}
	}
	marks := []mark{{step: "open", from: 0, to: 100}, {step: "still", from: 100, to: 200}, {step: "close", from: 200, to: 300}}
	r := buildReport(frametrace.Header{Device: "x"}, 1, marks, nil, []frametrace.Frame{
		frame(10, 0.5), frame(20, 0.96), frame(30, 1), frame(40, 1),
		frame(110, 1), frame(120, 1),
		frame(210, 0.5), frame(220, 0.01), frame(230, 0), frame(240, 0),
	})
	for _, want := range []struct {
		step            int
		nearly, settled time.Duration
	}{{0, 30, 40}, {2, 30, 40}} {
		m := r.Steps[want.step].Motion
		if m == nil || m.Nearly.P50 != want.nearly || m.Settled.P50 != want.settled {
			t.Errorf("step %s: motion %+v, want within 5%% at %v and settled at %v", r.Steps[want.step].Name, m, want.nearly, want.settled)
		}
	}
	if m := r.Steps[1].Motion; m != nil {
		t.Errorf("an overview that did not move reports %+v", m)
	}
	tail := buildReport(frametrace.Header{Device: "x"}, 1, marks[:1], nil, []frametrace.Frame{frame(10, 0.5), frame(20, 0.96), frame(30, 0.999)})
	if m := tail.Steps[0].Motion; m == nil || m.Nearly.P50 != 30 || m.Settled.Samples != 0 {
		t.Errorf("a spring still in its tail when the step ends: %+v", m)
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
	refused := measure(options{backend: "headless", renderer: "soft", compositor: binary, rounds: 1, only: []string{"glass-still"}}, io.Discard, os.Getenv)
	if refused == nil || !strings.Contains(refused.Error(), "no real GPU") {
		t.Fatalf("the software renderer passed for a GPU: %v", refused)
	}
	var text strings.Builder
	o := options{backend: "headless", renderer: "soft", compositor: binary, rounds: 1, only: []string{"window", "swipe", "glass-still", "overview-scrub-10"}, allowSoftware: true, jsonPath: jsonPath}
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
	if still.Name != "glass-over-still" || still.Summary.Presented > 1 {
		t.Fatalf("glass over nothing moving redrew: %+v", still.Summary)
	}
}

func TestAMissingCompositorFailsWithItsLog(t *testing.T) {
	script := filepath.Join(t.TempDir(), "lys-wm")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'no render node for you' >&2\nexit 3\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	err := measure(options{backend: "headless", renderer: "vulkan", compositor: script, rounds: 1}, io.Discard, os.Getenv)
	if err == nil || !strings.Contains(err.Error(), "did not start") || !strings.Contains(err.Error(), "no render node for you") || !strings.Contains(err.Error(), "/dev/dri") {
		t.Fatalf("error = %v", err)
	}
}

func TestOnlyGrowthThatRepeatsCounts(t *testing.T) {
	for _, c := range []struct {
		name    string
		samples []int64
		want    int64
	}{
		{"a leak of 11680 KiB a round", []int64{100000, 111680, 123360, 135040}, 11680},
		{"an arena that grows once", []int64{100000, 120000, 120100, 120200}, 100},
		{"a scavenge in the middle", []int64{100000, 100500, 99000, 100500}, -1500},
		{"two samples", []int64{100000, 100300}, 300},
	} {
		if got := repeatedGrowth(c.samples); got != c.want {
			t.Errorf("%s: %d KiB, want %d", c.name, got, c.want)
		}
	}
}

func TestALimitNeedsRoundsForGrowthToRepeat(t *testing.T) {
	for _, args := range [][]string{{"-gate", "-rounds", "4"}, {"-write-baseline", "b.json", "-rounds", "4"}} {
		if _, err := parseOptions(args, io.Discard); err == nil || !strings.Contains(err.Error(), "needs at least") {
			t.Errorf("%v: %v", args, err)
		}
	}
	if _, err := parseOptions([]string{"-gate", "-rounds", strconv.Itoa(gateRounds)}, io.Discard); err != nil {
		t.Error(err)
	}
}

func costReport(step string, cost frametrace.CostSummary, growth ...rssGrowth) *report {
	return &report{Header: frametrace.Header{Renderer: "gles", Software: true}, Steps: []stepReport{{Name: step, Summary: frametrace.Summary{Cost: cost}}}, Growth: growth}
}

func TestTheGateNamesEveryStepAndMetricOverItsLimit(t *testing.T) {
	var fine frametrace.CostSummary
	fine.Draws.Worst = 10
	b := baseline{"gles/software": derive(costReport("move", fine, rssGrowth{Scenario: "move", KiB: 100}))}
	b["gles/software"].Steps["genie"] = b["gles/software"].Steps["move"]
	if err := checkBaseline(b, costReport("move", fine, rssGrowth{Scenario: "move", KiB: 100})); err != nil {
		t.Fatalf("the run the limits came from: %v", err)
	}
	over := fine
	over.Draws.Worst, over.Passes.Worst = 1000, 1000
	r := costReport("move", over, rssGrowth{Scenario: "move", KiB: 1 << 30})
	r.Steps = append(r.Steps, stepReport{Name: "genie", Summary: frametrace.Summary{Cost: over}})
	err := checkBaseline(b, r)
	if err == nil {
		t.Fatal("nothing over its limit")
	}
	for _, want := range []string{"move: draws_per_frame 1000", "move: render_passes_per_frame 1000", "genie: draws_per_frame 1000", "genie: render_passes_per_frame 1000", "RSS grew"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q missing from: %v", want, err)
		}
	}
	if err := checkBaseline(b, costReport("unknown", fine)); err == nil || !strings.Contains(err.Error(), "unknown: no limits") {
		t.Fatalf("a step with no limits: %v", err)
	}
}

func TestAGPUIsNotHeldToTheSoftwareRenderersLimits(t *testing.T) {
	var c frametrace.CostSummary
	c.Filters.Worst = 8
	gpu := costReport("overview-10-open", c)
	gpu.RealGPU, gpu.Header.Software = true, false
	var none frametrace.CostSummary
	b := baseline{"gles/software": derive(costReport("overview-10-open", none))}
	err := checkBaseline(b, gpu)
	if err == nil || !strings.Contains(err.Error(), "no limits for gles/gpu") {
		t.Fatalf("a GPU against software limits: %v", err)
	}
	b["gles/gpu"] = derive(gpu)
	if err := checkBaseline(b, gpu); err != nil {
		t.Fatalf("a GPU against its own limits: %v", err)
	}
}

func TestDamageRectsAreLimitedByTheCapWhateverWasObserved(t *testing.T) {
	var c frametrace.CostSummary
	c.DamageRects.Worst = 31
	got := derive(costReport("move", c)).Steps["move"]["damage_rects_per_frame"]
	if got != damageRectCap {
		t.Fatalf("damage rect limit %d, want the cap %d", got, damageRectCap)
	}
}

func TestAGPUWaitIsOverALimitOfNone(t *testing.T) {
	var none frametrace.CostSummary
	b := baseline{"gles/software": derive(costReport("resize-grow", none))}
	if got := b["gles/software"].Steps["resize-grow"]["gpu_waits_per_frame"]; got != 0 {
		t.Fatalf("gpu wait limit %d from a run without any, want 0", got)
	}
	waited := none
	waited.GPUWaits.Worst = 1
	if err := checkBaseline(b, costReport("resize-grow", waited)); err == nil || !strings.Contains(err.Error(), "resize-grow: gpu_waits_per_frame 1, limit 0") {
		t.Fatalf("one GPU wait in a frame: %v", err)
	}
}

func TestEveryStepHasEveryLimitInEveryBaseline(t *testing.T) {
	for _, key := range []string{"gles/software", "vulkan/software"} {
		if _, ok := checkedInBaseline[key]; !ok {
			t.Errorf("no baseline for %s", key)
		}
	}
	for renderer, rb := range checkedInBaseline {
		for i := range scenarios {
			for _, st := range scenarios[i].steps {
				for j := range metrics {
					m := &metrics[j]
					if _, ok := rb.Steps[st.name][m.name]; m.gates(st.name) && !ok {
						t.Errorf("%s: %s has no limit for %s", renderer, st.name, m.name)
					}
				}
			}
		}
	}
}

func TestTheReportShowsSchedulerLatencyGCPausesAndTheNewCounts(t *testing.T) {
	f := frametrace.Frame{
		Output: "A", Kind: frametrace.Composited, Start: 10, Present: 20, Refresh: 10, Sched: 3 * time.Millisecond, GCPause: 7 * time.Millisecond,
		Cost: frametrace.Cost{Submits: 11, Targets: 12, FilterPx: 13, RepaintPx: 14, Copied: 15},
	}
	r := buildReport(frametrace.Header{Device: "x"}, 1, []mark{{step: "a", from: 0, to: 100}}, nil, []frametrace.Frame{f})
	var out strings.Builder
	r.write(&out)
	text := out.String()
	for _, want := range []string{"sched", "gc", "3.00 / 3.00 / 3.00 / 3.00", "7.00 / 7.00 / 7.00 / 7.00", "submits", "copied bytes"} {
		if !strings.Contains(text, want) {
			t.Errorf("the report lacks %q:\n%s", want, text)
		}
	}
	if !regexp.MustCompile(`a( +\d+){10} +11 +12 +13 +14 +15\n`).MatchString(text) {
		t.Errorf("the report lacks the new counts:\n%s", text)
	}
}

func TestTiledGlassFiltersNothingAfterTheFirstFrameOfItsAnimation(t *testing.T) {
	var settled frametrace.CostSummary
	settled.Filters.Worst, settled.Passes.Worst, settled.Passes.AfterFirst = 4, 6, 1
	b := baseline{"gles/software": derive(costReport("glass-tiles-slide-back", settled))}
	l := b["gles/software"].Steps["glass-tiles-slide-back"]
	if l["filter_passes_after_first_frame"] != 0 || l["render_passes_after_first_frame"] != 1 {
		t.Fatalf("limits %v: want no filter and only the frame's own pass after the first frame", l)
	}
	again := settled
	again.Filters.AfterFirst, again.Passes.AfterFirst = 4, 6
	err := checkBaseline(b, costReport("glass-tiles-slide-back", again))
	for _, want := range []string{"filter_passes_after_first_frame 4, limit 0", "render_passes_after_first_frame 6, limit 1"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("a chain refiltered on every frame: %v, want %q", err, want)
		}
	}
	if _, ok := derive(costReport("glass-slide-back", settled)).Steps["glass-slide-back"]["filter_passes_after_first_frame"]; ok {
		t.Error("a floating glass window blurs live and is held to no limit after the first frame")
	}
}

func TestABusyBandUnderGlassIsHeldToThePixelsItsReachTouches(t *testing.T) {
	var band frametrace.CostSummary
	band.FilterPx.Worst, band.RepaintPx.Worst = 400_000, 300_000
	b := baseline{"gles/software": derive(costReport("glass-over-busy", band))}
	l := b["gles/software"].Steps["glass-over-busy"]
	if l["filter_pixels_per_frame"] != 500_000 || l["repaint_pixels_per_frame"] != 375_000 {
		t.Fatalf("limits %v: want a quarter over what was measured", l)
	}
	whole := band
	whole.FilterPx.Worst, whole.RepaintPx.Worst = 1_382_400, 2_073_600
	err := checkBaseline(b, costReport("glass-over-busy", whole))
	for _, want := range []string{"filter_pixels_per_frame 1382400, limit 500000", "repaint_pixels_per_frame 2073600, limit 375000"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("a band that refilters and repaints the whole window: %v, want %q", err, want)
		}
	}
}

func TestASecondSubmitInAFrameIsOverTheLimitOfOne(t *testing.T) {
	var one frametrace.CostSummary
	one.Submits.Worst = 1
	b := baseline{"gles/software": derive(costReport("glass-over-busy", one))}
	if got := b["gles/software"].Steps["glass-over-busy"]["submits_per_frame"]; got != 1 {
		t.Fatalf("submit limit %d from a run of one a frame, want 1", got)
	}
	two := one
	two.Submits.Worst = 2
	if err := checkBaseline(b, costReport("glass-over-busy", two)); err == nil || !strings.Contains(err.Error(), "glass-over-busy: submits_per_frame 2, limit 1") {
		t.Fatalf("two submits in a frame: %v", err)
	}
}
