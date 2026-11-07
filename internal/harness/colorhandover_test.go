package harness

import (
	"bytes"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/handover"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/testclient"
)

func TestAnICCDescriptionIsHandedOverFromTheBytesRead(t *testing.T) {
	t.Parallel()
	h := StartWith(t, func(cfg *compositor.Config) { cfg.ColorManagement = true }, small)
	h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
		comp.Display().KeepHandoverJournal()
		return nil
	})
	c, m, _ := colorManager(t, h)
	profile := testICCProfile()
	const offset = 4096
	file := memfd(t, make([]byte, offset))
	if _, err := unix.Pwrite(file, profile, offset); err != nil {
		t.Fatal(err)
	}
	creator, err := m.CreateIccCreator()
	if err != nil {
		t.Fatal(err)
	}
	if err := creator.SetIccFile(file, offset, uint32(len(profile))); err != nil {
		t.Fatal(err)
	}
	ev := &descEvents{}
	desc, err := creator.Create(ev)
	if err != nil {
		t.Fatal(err)
	}
	c.DispatchUntil("the ICC description", func() bool { return ev.ready || ev.failed })
	if ev.failed {
		t.Fatalf("the profile failed: %s", ev.msg)
	}
	if err := unix.Ftruncate(file, 0); err != nil {
		t.Fatal(err)
	}
	kept := func() int {
		var n int
		h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
			n = comp.Display().Clients()[0].JournalFileBytes()
			return nil
		})
		return n
	}
	if n := kept(); n != len(profile) {
		t.Fatalf("the journal keeps %d bytes for a %d-byte profile", n, len(profile))
	}

	var replayed [][]byte
	h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
		var fds handover.FDs
		defer fds.CloseOwned()
		st, _, err := comp.Display().Snapshot(&fds, func(*server.Client) bool { return false })
		if err != nil {
			return err
		}
		for _, cs := range st.Clients {
			got, err := fds.GetAll(cs.StreamFDs)
			if err != nil {
				return err
			}
			for _, fd := range got {
				var stat unix.Stat_t
				if err := unix.Fstat(fd, &stat); err != nil {
					return err
				}
				data := make([]byte, stat.Size)
				if _, err := unix.Pread(fd, data, 0); err != nil {
					return err
				}
				replayed = append(replayed, data)
			}
		}
		return nil
	})
	want := append(make([]byte, offset), profile...)
	if len(replayed) != 1 || !bytes.Equal(replayed[0], want) {
		t.Fatalf("the stream passes %d fds, want one holding the %d-byte profile at %d", len(replayed), len(profile), offset)
	}

	if err := desc.Destroy(); err != nil {
		t.Fatal(err)
	}
	c.Sync()
	if n := kept(); n != 0 {
		t.Fatalf("the journal keeps %d bytes of a destroyed description", n)
	}
	w := c.Window("after", testclient.Fill(0xff, 0, 0))
	c.DispatchUntil("the window to map", w.Mapped)
}
