package anim

import (
	"math"
	"time"
)

const (
	wobbleStep    = 2 * time.Millisecond
	wobbleReach   = 0.35
	wobbleCouple  = 0.5
	wobbleRestPx  = 0.25
	wobbleRestVel = 2.0
)

type Wobble struct {
	cols, rows int
	off, vel   [][2]float64
	omega2     float64
	damping    float64
	last       time.Duration
}

func NewWobble(s Spring, cols, rows int, now time.Duration) *Wobble {
	n := (cols + 1) * (rows + 1)
	return &Wobble{cols: cols, rows: rows, off: make([][2]float64, n), vel: make([][2]float64, n), omega2: s.omega * s.omega, damping: 2 * s.zeta * s.omega, last: now}
}

func (w *Wobble) Grid() (cols, rows int) {
	return w.cols, w.rows
}

func (w *Wobble) node(i, j int) int {
	return j*(w.cols+1) + i
}

func (w *Wobble) Shift(dx, dy, grabU, grabV float64) {
	for j := range w.rows + 1 {
		for i := range w.cols + 1 {
			du, dv := float64(i)/float64(w.cols)-grabU, float64(j)/float64(w.rows)-grabV
			lag := 1 - math.Exp(-(du*du+dv*dv)/(wobbleReach*wobbleReach))
			k := w.node(i, j)
			w.off[k][0] -= dx * lag
			w.off[k][1] -= dy * lag
		}
	}
}

func (w *Wobble) step(dt float64) {
	acc := make([][2]float64, len(w.off))
	for j := range w.rows + 1 {
		for i := range w.cols + 1 {
			k := w.node(i, j)
			var sum [2]float64
			n := 0
			for _, d := range [4][2]int{{-1, 0}, {1, 0}, {0, -1}, {0, 1}} {
				ni, nj := i+d[0], j+d[1]
				if ni < 0 || nj < 0 || ni > w.cols || nj > w.rows {
					continue
				}
				o := w.off[w.node(ni, nj)]
				sum[0] += o[0]
				sum[1] += o[1]
				n++
			}
			for a := range 2 {
				pull := sum[a]/float64(n) - w.off[k][a]
				acc[k][a] = -w.omega2*w.off[k][a] + wobbleCouple*w.omega2*pull - w.damping*w.vel[k][a]
			}
		}
	}
	for k := range w.off {
		for a := range 2 {
			w.vel[k][a] += acc[k][a] * dt
			w.off[k][a] += w.vel[k][a] * dt
		}
	}
}

const wobbleHorizon = 5 * time.Second

func (w *Wobble) Advance(now time.Duration) {
	if now-w.last > wobbleHorizon {
		w.last = now - wobbleHorizon
	}
	for w.last+wobbleStep <= now {
		w.step(wobbleStep.Seconds())
		w.last += wobbleStep
	}
}

func (w *Wobble) Offset(i, j int) (dx, dy float64) {
	o := w.off[w.node(i, j)]
	return o[0], o[1]
}

func (w *Wobble) Resting() bool {
	for k := range w.off {
		if math.Hypot(w.off[k][0], w.off[k][1]) > wobbleRestPx || math.Hypot(w.vel[k][0], w.vel[k][1]) > wobbleRestVel {
			return false
		}
	}
	return true
}
