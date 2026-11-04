package harness

import (
	"errors"
	"testing"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/testclient"
)

func TestWithoutAHolderNoJournalIsKept(t *testing.T) {
	t.Parallel()
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("first", testclient.Fill(0xff, 0, 0))
	c.DispatchUntil("the window to map", w.Mapped)
	h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
		if records, messages := comp.Display().Clients()[0].JournalSize(); records != 0 || messages != 0 {
			t.Errorf("a session without a holder journals %d records and %d messages", records, messages)
		}
		return nil
	})
}

func TestAClientsHandoverJournalDoesNotGrowWithItsRequests(t *testing.T) {
	t.Parallel()
	h := Start(t, small)
	h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
		comp.Display().KeepHandoverJournal()
		return nil
	})
	c := testclient.Connect(t, h.Socket())
	w := c.Window("first", testclient.Fill(0xff, 0, 0))
	c.DispatchUntil("the window to map", w.Mapped)
	size := func() (records, messages int) {
		h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
			records, messages = comp.Display().Clients()[0].JournalSize()
			return nil
		})
		return records, messages
	}
	churn := func(n int) {
		for range n {
			region, err := c.Compositor.CreateRegion()
			if err != nil {
				t.Fatal(err)
			}
			if err := errors.Join(region.Add(0, 0, 10, 10), w.Surface.SetOpaqueRegion(region), region.Destroy(), w.Toplevel.SetTitle("again"), w.Surface.Commit(), c.Display.Flush()); err != nil {
				t.Fatal(err)
			}
			c.Sync()
		}
	}
	churn(10)
	records, messages := size()
	churn(300)
	if r, m := size(); r != records || m != messages {
		t.Fatalf("the journal grew from %d records and %d messages to %d and %d over 300 repeats", records, messages, r, m)
	}
}
