package frametrace

import (
	"math"
	"runtime/metrics"
	"slices"
	"time"
)

const (
	schedLatencies = "/sched/latencies:seconds"
	gcPauses       = "/sched/pauses/total/gc:seconds"
)

type runtimeStats struct {
	samples [2]metrics.Sample
	seen    [2][]uint64
}

func newRuntimeStats() *runtimeStats {
	s := &runtimeStats{}
	s.samples[0].Name, s.samples[1].Name = schedLatencies, gcPauses
	s.read()
	return s
}

func (s *runtimeStats) read() (sched, gc time.Duration) {
	metrics.Read(s.samples[:])
	return s.worstNew(0), s.worstNew(1)
}

func (s *runtimeStats) worstNew(i int) time.Duration {
	v := s.samples[i].Value
	if v.Kind() != metrics.KindFloat64Histogram {
		return 0
	}
	h := v.Float64Histogram()
	seen := s.seen[i]
	if len(seen) != len(h.Counts) {
		s.seen[i] = slices.Clone(h.Counts)
		return 0
	}
	var worst time.Duration
	for b, n := range slices.Backward(h.Counts) {
		if n > seen[b] {
			worst = bucketTime(h.Buckets, b)
			break
		}
	}
	copy(seen, h.Counts)
	return worst
}

func bucketTime(buckets []float64, b int) time.Duration {
	edge := buckets[b+1]
	if math.IsInf(edge, 1) {
		edge = buckets[b]
	}
	return time.Duration(edge * float64(time.Second))
}
