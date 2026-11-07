package compositor

import (
	"slices"
	"testing"

	"github.com/stubbedev/lys-wm/internal/wm"
)

func TestMostRecentlyUsedOrder(t *testing.T) {
	var m mru
	for _, id := range []wm.WindowID{1, 2, 3, 2} {
		m.record(id)
	}
	if !slices.Equal(m.order, []wm.WindowID{2, 3, 1}) {
		t.Fatalf("order %v", m.order)
	}
	if m.stamp(2) != 4 || m.stamp(3) != 3 || m.stamp(1) != 1 || m.stamp(9) != 0 {
		t.Fatalf("stamps %v", m.stamps)
	}
	m.forget(3)
	if !slices.Equal(m.order, []wm.WindowID{2, 1}) || m.stamp(3) != 0 {
		t.Fatalf("after forgetting 3: %v %v", m.order, m.stamps)
	}
}
