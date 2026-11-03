package frametrace

import (
	"slices"
	"time"
)

type Stat struct {
	Samples int           `json:"samples"`
	P50     time.Duration `json:"p50_ns"`
	P95     time.Duration `json:"p95_ns"`
	P99     time.Duration `json:"p99_ns"`
	Worst   time.Duration `json:"worst_ns"`
}

func NewStat(samples []time.Duration) Stat {
	if len(samples) == 0 {
		return Stat{}
	}
	sorted := slices.Sorted(slices.Values(samples))
	rank := func(percent int) time.Duration {
		return sorted[(len(sorted)*percent+99)/100-1]
	}
	return Stat{Samples: len(sorted), P50: rank(50), P95: rank(95), P99: rank(99), Worst: sorted[len(sorted)-1]}
}

type Summary struct {
	Frames        int         `json:"frames"`
	Presented     int         `json:"presented"`
	Empty         int         `json:"empty"`
	Scanouts      int         `json:"scanouts"`
	Dropped       int         `json:"dropped"`
	Late          int         `json:"late"`
	LatePercent   float64     `json:"late_percent"`
	SkippedVblank int         `json:"skipped_vblanks"`
	Interval      Stat        `json:"interval"`
	CPU           Stat        `json:"cpu"`
	GPU           Stat        `json:"gpu"`
	Latency       Stat        `json:"latency"`
	Cost          CostSummary `json:"cost"`
}

type CostStat struct {
	P50      uint64  `json:"p50"`
	Worst    uint64  `json:"worst"`
	Mean     float64 `json:"mean"`
	RunWorst uint64  `json:"run_worst"`
}

type CostSummary struct {
	Draws       CostStat `json:"draws"`
	Passes      CostStat `json:"passes"`
	Filters     CostStat `json:"filters"`
	Uploads     CostStat `json:"uploads"`
	Thumbs      CostStat `json:"thumbs"`
	DamageRects CostStat `json:"damage_rects"`
	Allocs      CostStat `json:"allocs"`
	FileIO      CostStat `json:"file_io"`
}

func costStat(runs [][]Frame, of func(*Cost) uint64) CostStat {
	var s CostStat
	var all []uint64
	var total uint64
	for _, frames := range runs {
		var run uint64
		for i := range frames {
			if frames[i].Kind != Composited {
				continue
			}
			v := of(&frames[i].Cost)
			all = append(all, v)
			run += v
		}
		total += run
		s.RunWorst = max(s.RunWorst, run)
	}
	if len(all) == 0 {
		return s
	}
	slices.Sort(all)
	s.P50, s.Worst, s.Mean = all[(len(all)-1)/2], all[len(all)-1], float64(total)/float64(len(all))
	return s
}

func summarizeCost(runs [][]Frame) CostSummary {
	return CostSummary{
		Draws:       costStat(runs, func(c *Cost) uint64 { return c.Draws }),
		Passes:      costStat(runs, func(c *Cost) uint64 { return c.Passes }),
		Filters:     costStat(runs, func(c *Cost) uint64 { return c.Filters }),
		Uploads:     costStat(runs, func(c *Cost) uint64 { return c.Uploads }),
		Thumbs:      costStat(runs, func(c *Cost) uint64 { return c.Thumbs }),
		DamageRects: costStat(runs, func(c *Cost) uint64 { return c.DamageRects }),
		Allocs:      costStat(runs, func(c *Cost) uint64 { return c.Allocs }),
		FileIO:      costStat(runs, func(c *Cost) uint64 { return c.FileIO }),
	}
}

func (f *Frame) Shown() time.Duration {
	if f.GPUDone <= f.Present || f.Refresh <= 0 {
		return f.Present
	}
	return f.Present + (f.GPUDone-f.Present+f.Refresh-1)/f.Refresh*f.Refresh
}

func (f *Frame) SkippedVblanks() int {
	if f.Aim == 0 || f.Refresh <= 0 {
		return 0
	}
	return max(int((f.Shown()-f.Aim+f.Refresh/2)/f.Refresh), 0)
}

func Summarize(runs [][]Frame, continuous bool) Summary {
	var s Summary
	var interval, cpu, gpu, latency []time.Duration
	for _, frames := range runs {
		s.Frames += len(frames)
		last := map[string]*Frame{}
		for i := range frames {
			f := &frames[i]
			cpu = append(cpu, f.CPU)
			prev := last[f.Output]
			last[f.Output] = f
			switch f.Kind {
			case Empty:
				s.Empty++
				continue
			case Dropped:
				s.Dropped++
				continue
			case Scanout:
				s.Scanouts++
			case Composited:
				if f.GPUSource != GPUNone {
					gpu = append(gpu, f.GPU)
				}
			}
			s.Presented++
			shown := f.Shown()
			latency = append(latency, shown-f.Start)
			if skipped := f.SkippedVblanks(); skipped > 0 {
				s.Late++
				s.SkippedVblank += skipped
			}
			if prev != nil && prev.Present != 0 && (prev.Animating || continuous) && !f.Torn {
				interval = append(interval, shown-prev.Shown())
			}
		}
	}
	if s.Presented > 0 {
		s.LatePercent = float64(s.Late) * 100 / float64(s.Presented)
	}
	s.Interval, s.CPU, s.GPU, s.Latency = NewStat(interval), NewStat(cpu), NewStat(gpu), NewStat(latency)
	s.Cost = summarizeCost(runs)
	return s
}
