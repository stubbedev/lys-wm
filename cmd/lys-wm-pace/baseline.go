package main

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/stubbedev/lys-wm/internal/frametrace"
)

type limits map[string]uint64

type rendererBaseline struct {
	Steps map[string]limits `json:"steps"`
}

type baseline map[string]rendererBaseline

const (
	damageRectCap     = 32
	slackAllocs       = 32
	slackCount        = 2
	repeatedGrowthKiB = 1024
	marginDenominator = 4
)

type metric struct {
	name  string
	of    func(*frametrace.CostSummary) uint64
	slack uint64
	fixed uint64
	steps string
}

func (m *metric) gates(step string) bool {
	return strings.HasPrefix(step, m.steps)
}

var metrics = []metric{
	{name: "draws_per_frame", of: func(c *frametrace.CostSummary) uint64 { return c.Draws.Worst }, slack: slackCount},
	{name: "render_passes_per_frame", of: func(c *frametrace.CostSummary) uint64 { return c.Passes.Worst }, slack: slackCount},
	{name: "filter_passes_per_frame", of: func(c *frametrace.CostSummary) uint64 { return c.Filters.Worst }, slack: slackCount},
	{name: "filter_passes_per_run", of: func(c *frametrace.CostSummary) uint64 { return c.Filters.RunWorst }, slack: slackCount, steps: "overview"},
	{name: "render_passes_per_scanout_frame", of: func(c *frametrace.CostSummary) uint64 { return c.Passes.Worst }, steps: scaledTitle},
	{name: "render_passes_after_first_frame", of: func(c *frametrace.CostSummary) uint64 { return c.Passes.AfterFirst }, steps: "glass-tiles"},
	{name: "filter_passes_after_first_frame", of: func(c *frametrace.CostSummary) uint64 { return c.Filters.AfterFirst }, steps: "glass-tiles"},
	{name: "filter_pixels_per_frame", of: func(c *frametrace.CostSummary) uint64 { return c.FilterPx.Worst }, steps: "glass-over-busy"},
	{name: "repaint_pixels_per_frame", of: func(c *frametrace.CostSummary) uint64 { return c.RepaintPx.Worst }, steps: "glass-over-busy"},
	{name: "uploads_per_frame", of: func(c *frametrace.CostSummary) uint64 { return c.Uploads.Worst }, slack: slackCount},
	{name: "gpu_waits_per_frame", of: func(c *frametrace.CostSummary) uint64 { return c.GPUWaits.Worst }},
	{name: "submits_per_frame", of: func(c *frametrace.CostSummary) uint64 { return c.Submits.Worst }},
	{name: "thumb_draws_per_settle", of: func(c *frametrace.CostSummary) uint64 { return c.Thumbs.RunWorst }, slack: slackCount},
	{name: "damage_rects_per_frame", of: func(c *frametrace.CostSummary) uint64 { return c.DamageRects.Worst }, slack: slackCount, fixed: damageRectCap},
	{name: "allocs_per_frame", of: func(c *frametrace.CostSummary) uint64 { return c.Allocs.Repeated }, slack: slackAllocs},
	{name: "file_io_per_frame", of: func(c *frametrace.CostSummary) uint64 { return c.FileIO.P50 }, slack: slackCount},
}

//go:embed baseline.json
var baselineJSON []byte

var checkedInBaseline = mustParseBaseline(baselineJSON)

func mustParseBaseline(data []byte) baseline {
	var b baseline
	if err := json.Unmarshal(data, &b); err != nil {
		panic(fmt.Errorf("the checked-in baseline.json: %w", err))
	}
	return b
}

func withMargin(observed, slack, fixed uint64) uint64 {
	if fixed > 0 {
		return fixed
	}
	return observed + max(observed/marginDenominator, slack)
}

func derive(r *report) rendererBaseline {
	b := rendererBaseline{Steps: make(map[string]limits, len(r.Steps))}
	for i := range r.Steps {
		st := &r.Steps[i]
		l := make(limits, len(metrics))
		for i := range metrics {
			m := &metrics[i]
			if m.gates(st.Name) {
				l[m.name] = withMargin(m.of(&st.Summary.Cost), m.slack, m.fixed)
			}
		}
		b.Steps[st.Name] = l
	}
	return b
}

func baselineKey(r *report) string {
	class := "software"
	if r.RealGPU {
		class = "gpu"
	}
	return r.Header.Renderer + "/" + class
}

func updateBaseline(path string, r *report) error {
	current := baseline{}
	data, err := os.ReadFile(path) //nolint:gosec // the path comes from the command line
	switch {
	case err == nil:
		if err := json.Unmarshal(data, &current); err != nil {
			return fmt.Errorf("read the baseline %s: %w", path, err)
		}
	case !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("read the baseline: %w", err)
	}
	current[baselineKey(r)] = derive(r)
	out, err := json.MarshalIndent(current, "", "  ")
	if err != nil {
		return fmt.Errorf("encode the baseline: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(out, '\n'), 0o600); err != nil {
		return fmt.Errorf("write the baseline: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("write the baseline: %w", err)
	}
	return nil
}

func checkBaseline(b baseline, r *report) error {
	key := baselineKey(r)
	rb, ok := b[key]
	if !ok {
		return fmt.Errorf("the baseline has no limits for %s: write them with `just docker pace-baseline` on a GPU, `just pace-baseline-software` otherwise", key)
	}
	var over []string
	for i := range r.Steps {
		st := &r.Steps[i]
		l, ok := rb.Steps[st.Name]
		if !ok {
			over = append(over, st.Name+": no limits in the baseline")
			continue
		}
		for i := range metrics {
			m := &metrics[i]
			if !m.gates(st.Name) {
				continue
			}
			limit, ok := l[m.name]
			if !ok {
				over = append(over, st.Name+": no limit for "+m.name)
				continue
			}
			if got := m.of(&st.Summary.Cost); got > limit {
				over = append(over, fmt.Sprintf("%s: %s %d, limit %d", st.Name, m.name, got, limit))
			}
		}
	}
	for _, g := range r.Growth {
		if g.KiB > repeatedGrowthKiB {
			over = append(over, fmt.Sprintf("%s: RSS grew at least %d KiB in every round, limit %d", g.Scenario, g.KiB, repeatedGrowthKiB))
		}
	}
	if len(over) > 0 {
		slices.Sort(over)
		return fmt.Errorf("over the checked-in baseline:\n  %s", strings.Join(over, "\n  "))
	}
	return nil
}
