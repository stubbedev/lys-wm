package main

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"slices"
	"strings"

	"github.com/stubbedev/lys-wm/internal/frametrace"
)

type limits map[string]uint64

type rendererBaseline struct {
	Steps  map[string]limits `json:"steps"`
	Growth limits            `json:"rss_growth_kib"`
}

type baseline map[string]rendererBaseline

const (
	damageRectCap     = 32
	slackAllocs       = 32
	slackCount        = 2
	slackGrowthKiB    = 16384
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
	{name: "uploads_per_frame", of: func(c *frametrace.CostSummary) uint64 { return c.Uploads.Worst }, slack: slackCount},
	{name: "thumb_draws_per_settle", of: func(c *frametrace.CostSummary) uint64 { return c.Thumbs.RunWorst }, slack: slackCount},
	{name: "damage_rects_per_frame", of: func(c *frametrace.CostSummary) uint64 { return c.DamageRects.Worst }, slack: slackCount, fixed: damageRectCap},
	{name: "allocs_per_frame", of: func(c *frametrace.CostSummary) uint64 { return c.Allocs.Worst }, slack: slackAllocs},
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
	b := rendererBaseline{Steps: make(map[string]limits, len(r.Steps)), Growth: make(limits, len(r.Growth))}
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
	for _, g := range r.Growth {
		b.Growth[g.Scenario] = withMargin(uint64(max(g.KiB, 0)), slackGrowthKiB, 0)
	}
	return b
}

func updateBaseline(path, renderer string, r *report) error {
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
	current[renderer] = derive(r)
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

func checkBaseline(b baseline, renderer string, r *report) error {
	rb, ok := b[renderer]
	if !ok {
		return fmt.Errorf("the baseline has no limits for the %s renderer: measure it with -write-baseline cmd/lys-wm-pace/baseline.json", renderer)
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
		limit, ok := rb.Growth[g.Scenario]
		if !ok {
			limit = math.MaxUint64
			over = append(over, g.Scenario+": no RSS growth limit in the baseline")
		}
		if g.KiB > 0 && uint64(g.KiB) > limit {
			over = append(over, fmt.Sprintf("%s: RSS grew %d KiB over its rounds, limit %d", g.Scenario, g.KiB, limit))
		}
	}
	if len(over) > 0 {
		slices.Sort(over)
		return fmt.Errorf("over the checked-in baseline:\n  %s", strings.Join(over, "\n  "))
	}
	return nil
}
