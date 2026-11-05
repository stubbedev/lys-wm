package drm

import (
	"testing"

	"github.com/stubbedev/lys-lib/fourcc"

	"github.com/stubbedev/lys-wm/internal/render/soft"
	"github.com/stubbedev/lys-wm/internal/wm"
)

func contentTypeOf(c commit) (uint64, bool) {
	v, ok := c.values[[2]uint32{20, propContentTypeID}]
	return v, ok
}

func (r *rig) nextFrame(o *Output) commit {
	r.t.Helper()
	r.dev.commits = nil
	if err := o.ScheduleFrame(); err != nil {
		r.t.Fatal(err)
	}
	r.committedSince("a frame", o, 0)
	c := r.dev.commits[len(r.dev.commits)-1]
	r.dev.flip(r.t, 10, 1)
	r.until("the flip", func() bool { return !o.flipping })
	return c
}

func TestTheContentTypeReachesTheConnectorOnlyWhenItChanges(t *testing.T) {
	r := boot(t, func(d *fakeDevice) allocator {
		d.contentTypes = true
		return &fakeAlloc{soft: soft.New()}
	})
	o := r.b.Outputs()[0]
	if v, ok := contentTypeOf(r.nextFrame(o)); !ok || v != 0 {
		t.Fatalf("the modeset stated content type %d (%v), want No Data", v, ok)
	}
	if _, ok := contentTypeOf(r.nextFrame(o)); ok {
		t.Fatal("a flip restated an unchanged content type")
	}
	o.SetContentType(wm.ContentGame)
	o.SetContentType(wm.ContentGame)
	if v, ok := contentTypeOf(r.nextFrame(o)); !ok || v != 4 {
		t.Fatalf("a game filling the output set content type %d (%v), want Game", v, ok)
	}
	if _, ok := contentTypeOf(r.nextFrame(o)); ok {
		t.Fatal("the game's next frame set the content type again")
	}
	o.SetContentType(wm.ContentNone)
	r.dev.commits = nil
	k := clientBuffer(7, fourcc.Linear)
	if !r.scanout(o, k, false) {
		t.Fatal("the client buffer did not scan out")
	}
	if v, ok := contentTypeOf(r.dev.commits[len(r.dev.commits)-1]); !ok || v != 0 {
		t.Fatalf("leaving the game set content type %d (%v) on the scanout, want No Data", v, ok)
	}
}

func TestAConnectorWithoutAContentTypeNeverCarriesOne(t *testing.T) {
	r := boot(t, func(d *fakeDevice) allocator {
		d.asyncFlips = true
		return &fakeAlloc{soft: soft.New()}
	})
	o := r.lit()
	o.SetContentType(wm.ContentGame)
	if o.contentDirty {
		t.Fatal("a content type is pending on a connector without the property")
	}
	if !r.scanout(o, clientBuffer(7, fourcc.Linear), true) || !o.torn {
		t.Fatal("a pending content type kept a tearing scanout from flipping async")
	}
}
