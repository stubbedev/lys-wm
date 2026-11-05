package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/stubbedev/lys-wm/internal/frametrace"
)

type stepReport struct {
	Name       string               `json:"name"`
	Continuous bool                 `json:"continuous"`
	Summary    frametrace.Summary   `json:"summary"`
	Runs       [][]frametrace.Frame `json:"runs"`
	Motion     *overviewMotion      `json:"overview_motion,omitempty"`
}

type rssGrowth struct {
	Scenario string `json:"scenario"`
	KiB      int64  `json:"kib"`
}

type report struct {
	Header  frametrace.Header `json:"header"`
	RealGPU bool              `json:"real_gpu"`
	Rounds  int               `json:"rounds"`
	Steps   []stepReport      `json:"steps"`
	Growth  []rssGrowth       `json:"rss_growth"`
}

func repeatedGrowth(samples []int64) int64 {
	smallest := samples[1] - samples[0]
	for i := 2; i < len(samples); i++ {
		smallest = min(smallest, samples[i]-samples[i-1])
	}
	return smallest
}

func buildReport(h frametrace.Header, rounds int, marks []mark, growth []rssGrowth, frames []frametrace.Frame) report {
	r := report{Header: h, RealGPU: !h.Software, Rounds: rounds, Growth: growth}
	index := map[string]int{}
	acted := map[string][]time.Duration{}
	for _, m := range marks {
		if _, ok := index[m.step]; !ok {
			index[m.step] = len(r.Steps)
			r.Steps = append(r.Steps, stepReport{Name: m.step, Continuous: m.continuous})
		}
		st := &r.Steps[index[m.step]]
		st.Runs = append(st.Runs, framesIn(frames, m))
		acted[m.step] = append(acted[m.step], m.from)
	}
	for i := range r.Steps {
		r.Steps[i].Summary = frametrace.Summarize(r.Steps[i].Runs, r.Steps[i].Continuous)
		r.Steps[i].Motion = motionOf(r.Steps[i].Runs, acted[r.Steps[i].Name])
	}
	return r
}

func framesIn(frames []frametrace.Frame, m mark) []frametrace.Frame {
	var in []frametrace.Frame
	for i := range frames {
		if frames[i].Start >= m.from && frames[i].Start < m.to {
			in = append(in, frames[i])
		}
	}
	return in
}

func spread(s frametrace.Stat) string {
	if s.Samples == 0 {
		return "-"
	}
	return strings.Join([]string{millis(s.P50), millis(s.P95), millis(s.P99), millis(s.Worst)}, " / ")
}

func (r *report) write(out io.Writer) {
	clock := "simulated vsync: present times come from a timer, not a display"
	if r.Header.Vsync {
		clock = "real vsync: present times come from page flips"
	}
	fmt.Fprintf(out, "lys-wm-pace: %s on %s, %s backend, %s\n", r.Header.Renderer, r.Header.Device, r.Header.Backend, clock)
	if !r.RealGPU {
		fmt.Fprintln(out, "SOFTWARE RASTERISER: relative numbers and hot spots only, nothing here is what a GPU would do")
	}
	fmt.Fprintf(out, "%d rounds; times in ms as p50 / p95 / p99 / worst\n\n", r.Rounds)
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "step\tframes\tempty\tdropped\tlate\tskipped\tinterval\tcpu\tgpu\tlatency\tsched\tgc")
	for i := range r.Steps {
		st := &r.Steps[i]
		s := &st.Summary
		fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%d (%.1f%%)\t%d\t%s\t%s\t%s\t%s\t%s\t%s\n", st.Name, s.Frames, s.Empty, s.Dropped, s.Late, s.LatePercent, s.SkippedVblank, spread(s.Interval), spread(s.CPU), spread(s.GPU), spread(s.Latency), spread(s.Sched), spread(s.GCPause))
	}
	_ = w.Flush()
	fmt.Fprintln(out)
	r.writeCost(out)
	fmt.Fprintln(out)
	r.writeMotion(out)
}

func (r *report) writeCost(out io.Writer) {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "step\tdraws\tpasses\tfilters\tuploads\tgpu waits\tdamage rects\tthumbs/run\tfilters/run\tallocs\tfile io\tsubmits\ttargets\tfilter px\trepaint px\tcopied bytes")
	for i := range r.Steps {
		c := &r.Steps[i].Summary.Cost
		fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\n", r.Steps[i].Name, c.Draws.Worst, c.Passes.Worst, c.Filters.Worst, c.Uploads.Worst, c.GPUWaits.Worst, c.DamageRects.Worst, c.Thumbs.RunWorst, c.Filters.RunWorst, c.Allocs.Repeated, c.FileIO.P50, c.Submits.Worst, c.Targets.Worst, c.FilterPx.Worst, c.RepaintPx.Worst, c.Copied.Worst)
	}
	_ = w.Flush()
	for _, g := range r.Growth {
		fmt.Fprintf(out, "rss growth in every round of %s: %d KiB\n", g.Scenario, g.KiB)
	}
}

func (r *report) save(path string) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("encode the report: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp*")
	if err != nil {
		return fmt.Errorf("write the report: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	_, err = tmp.Write(append(data, '\n'))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		return fmt.Errorf("write the report to %s: %w", path, err)
	}
	return nil
}

const overviewNearly = 0.05

type overviewMotion struct {
	Nearly  frametrace.Stat `json:"nearly"`
	Settled frametrace.Stat `json:"settled"`
}

func overviewTarget(frames []frametrace.Frame) (float64, bool) {
	if len(frames) == 0 {
		return 0, false
	}
	from, to := frames[0].Cost.Overview, frames[len(frames)-1].Cost.Overview
	target := math.Round(to)
	return target, from != to && math.Abs(to-target) <= overviewNearly
}

func motionTimes(frames []frametrace.Frame, target float64, from time.Duration) (nearly, settled time.Duration) {
	nearly, settled = -1, -1
	for i := range frames {
		f := &frames[i]
		if f.Present == 0 {
			continue
		}
		left := math.Abs(f.Cost.Overview - target)
		if nearly < 0 && left <= overviewNearly {
			nearly = f.Present - from
		}
		if left == 0 {
			settled = f.Present - from
			break
		}
	}
	return nearly, settled
}

func motionOf(runs [][]frametrace.Frame, acted []time.Duration) *overviewMotion {
	var nearly, settled []time.Duration
	for i, run := range runs {
		target, moved := overviewTarget(run)
		if !moved {
			continue
		}
		n, s := motionTimes(run, target, acted[i])
		if n >= 0 {
			nearly = append(nearly, n)
		}
		if s >= 0 {
			settled = append(settled, s)
		}
	}
	if len(nearly) == 0 {
		return nil
	}
	return &overviewMotion{Nearly: frametrace.NewStat(nearly), Settled: frametrace.NewStat(settled)}
}

func (r *report) writeMotion(out io.Writer) {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "overview step\twithin 5%\tsettled")
	for i := range r.Steps {
		if m := r.Steps[i].Motion; m != nil {
			fmt.Fprintf(w, "%s\t%s\t%s\n", r.Steps[i].Name, spread(m.Nearly), spread(m.Settled))
		}
	}
	_ = w.Flush()
}
