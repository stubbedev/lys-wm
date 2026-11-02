package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/stubbedev/lys-wm/internal/frametrace"
)

type stepReport struct {
	Name       string               `json:"name"`
	Continuous bool                 `json:"continuous"`
	Ungated    bool                 `json:"ungated"`
	Summary    frametrace.Summary   `json:"summary"`
	Runs       [][]frametrace.Frame `json:"runs"`
}

type report struct {
	Header  frametrace.Header `json:"header"`
	RealGPU bool              `json:"real_gpu"`
	Rounds  int               `json:"rounds"`
	Steps   []stepReport      `json:"steps"`
}

func buildReport(h frametrace.Header, rounds int, marks []mark, frames []frametrace.Frame) report {
	r := report{Header: h, RealGPU: !h.Software, Rounds: rounds}
	index := map[string]int{}
	for _, m := range marks {
		if _, ok := index[m.step]; !ok {
			index[m.step] = len(r.Steps)
			r.Steps = append(r.Steps, stepReport{Name: m.step, Continuous: m.continuous, Ungated: m.ungated})
		}
		st := &r.Steps[index[m.step]]
		st.Runs = append(st.Runs, framesIn(frames, m))
	}
	for i := range r.Steps {
		r.Steps[i].Summary = frametrace.Summarize(r.Steps[i].Runs, r.Steps[i].Continuous)
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
	fmt.Fprintln(w, "step\tframes\tempty\tdropped\tlate\tskipped\tinterval\tcpu\tgpu\tlatency")
	for i := range r.Steps {
		st := &r.Steps[i]
		s := &st.Summary
		fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%d (%.1f%%)\t%d\t%s\t%s\t%s\t%s\n", st.Name, s.Frames, s.Empty, s.Dropped, s.Late, s.LatePercent, s.SkippedVblank, spread(s.Interval), spread(s.CPU), spread(s.GPU), spread(s.Latency))
	}
	_ = w.Flush()
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

func (r *report) gate(maxLatePercent float64) error {
	if maxLatePercent < 0 {
		return nil
	}
	var over []string
	for i := range r.Steps {
		if s := &r.Steps[i].Summary; !r.Steps[i].Ungated && s.LatePercent > maxLatePercent {
			over = append(over, fmt.Sprintf("%s %.1f%%", r.Steps[i].Name, s.LatePercent))
		}
	}
	if len(over) > 0 {
		return fmt.Errorf("over %.1f%% of frames late: %s", maxLatePercent, strings.Join(over, ", "))
	}
	return nil
}
