package session

import (
	"sync"
	"sync/atomic"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/tabletinput"
)

const (
	inputQueueBound = 1 << 16
	spareBatches    = 2
)

type arena[T any] struct{ vals []T }

func (a *arena[T]) put(v T) *T {
	a.vals = append(a.vals, v)
	return &a.vals[len(a.vals)-1]
}

func (a *arena[T]) reset() {
	clear(a.vals)
	a.vals = a.vals[:0]
}

type translated struct {
	key         arena[backend.Key]
	motion      arena[backend.PointerMotion]
	absolute    arena[backend.PointerMotionAbsolute]
	button      arena[backend.PointerButton]
	axis        arena[backend.PointerAxis]
	frame       arena[backend.PointerFrame]
	touchDown   arena[backend.TouchDown]
	touchMotion arena[backend.TouchMotion]
	touchUp     arena[backend.TouchUp]
	touchCancel arena[backend.TouchCancel]
	touchFrame  arena[backend.TouchFrame]
	swipe       arena[backend.SwipeUpdate]
	pinch       arena[backend.PinchUpdate]
	proximity   arena[tabletinput.Proximity]
	toolMotion  arena[tabletinput.Motion]
	tip         arena[tabletinput.Tip]
	toolButton  arena[tabletinput.Button]
	padButton   arena[tabletinput.PadButton]
	ring        arena[tabletinput.PadRing]
	strip       arena[tabletinput.PadStrip]
	dial        arena[tabletinput.PadDial]
}

func (t *translated) reset() {
	t.key.reset()
	t.motion.reset()
	t.absolute.reset()
	t.button.reset()
	t.axis.reset()
	t.frame.reset()
	t.touchDown.reset()
	t.touchMotion.reset()
	t.touchUp.reset()
	t.touchCancel.reset()
	t.touchFrame.reset()
	t.swipe.reset()
	t.pinch.reset()
	t.proximity.reset()
	t.toolMotion.reset()
	t.tip.reset()
	t.toolButton.reset()
	t.padButton.reset()
	t.ring.reset()
	t.strip.reset()
	t.dial.reset()
}

type recordKind uint8

const (
	deviceAdded recordKind = iota
	deviceRemoved
	inputEvent
	inputReport
	inputFailure
)

type inputRecord struct {
	kind recordKind
	dev  *InputDevice
	ev   backend.Event
	err  error
}

type inputBatch struct {
	records []inputRecord
	inputs  int
	dropped int
	events  translated
}

func (b *inputBatch) device(kind recordKind, d *InputDevice) {
	b.records = append(b.records, inputRecord{kind: kind, dev: d})
}

func (b *inputBatch) input(d *InputDevice, e backend.Event) {
	b.records = append(b.records, inputRecord{kind: inputEvent, dev: d, ev: e})
	b.inputs++
}

func (b *inputBatch) report(err error) {
	b.records = append(b.records, inputRecord{kind: inputReport, err: err})
}

func (b *inputBatch) fail(err error) {
	b.records = append(b.records, inputRecord{kind: inputFailure, err: err})
}

func (b *inputBatch) empty() bool {
	return len(b.records) == 0 && b.dropped == 0
}

func (b *inputBatch) reset() {
	clear(b.records)
	b.records = b.records[:0]
	b.inputs, b.dropped = 0, 0
	b.events.reset()
}

type inputQueue struct {
	mu     sync.Mutex
	ready  []*inputBatch
	spare  []*inputBatch
	posted bool
	held   atomic.Int64
}

func (q *inputQueue) full(filling *inputBatch) bool {
	return q.held.Load()+int64(filling.inputs) >= inputQueueBound
}

func (q *inputQueue) push(b *inputBatch) (next *inputBatch, wake bool) {
	q.held.Add(int64(b.inputs))
	q.mu.Lock()
	defer q.mu.Unlock()
	q.ready = append(q.ready, b)
	wake, q.posted = !q.posted, true
	if n := len(q.spare); n > 0 {
		next = q.spare[n-1]
		q.spare[n-1] = nil
		q.spare = q.spare[:n-1]
		return next, wake
	}
	return &inputBatch{}, wake
}

func (q *inputQueue) take(into []*inputBatch) []*inputBatch {
	q.mu.Lock()
	defer q.mu.Unlock()
	taken := q.ready
	q.ready, q.posted = into[:0], false
	return taken
}

func (q *inputQueue) recycle(batches []*inputBatch) {
	for _, b := range batches {
		q.held.Add(-int64(b.inputs))
		b.reset()
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, b := range batches {
		if len(q.spare) == spareBatches {
			return
		}
		q.spare = append(q.spare, b)
	}
}
