package main

import (
	"encoding/binary"
	"errors"
	"image"
	"math"
	"slices"
	"testing"

	"golang.org/x/sys/unix"

	ccm "github.com/stubbedev/lys-wm/internal/protocol/client/colormanagement"
	cw "github.com/stubbedev/lys-wm/internal/protocol/client/wayland"
	cwsc "github.com/stubbedev/lys-wm/internal/protocol/client/wlrscreencopy"
	"github.com/stubbedev/lys-wm/internal/testclient"
)

type colorEvents struct {
	done             bool
	outputChanged    int
	preferredChanged int
	outputEvents     []string
}

func (*colorEvents) Geometry(*cw.WlOutput, int32, int32, int32, int32, cw.WlOutputSubpixel, string, string, cw.WlOutputTransform) error {
	return nil
}

func (*colorEvents) Mode(*cw.WlOutput, cw.WlOutputMode, int32, int32, int32) error { return nil }

func (*colorEvents) Scale(*cw.WlOutput, int32) error { return nil }

func (*colorEvents) Name(*cw.WlOutput, string) error { return nil }

func (*colorEvents) Description(*cw.WlOutput, string) error { return nil }

func (*colorEvents) SupportedIntent(*ccm.WpColorManagerV1, ccm.WpColorManagerV1RenderIntent) error {
	return nil
}

func (*colorEvents) SupportedFeature(*ccm.WpColorManagerV1, ccm.WpColorManagerV1Feature) error {
	return nil
}

func (*colorEvents) SupportedTfNamed(*ccm.WpColorManagerV1, ccm.WpColorManagerV1TransferFunction) error {
	return nil
}

func (*colorEvents) SupportedPrimariesNamed(*ccm.WpColorManagerV1, ccm.WpColorManagerV1Primaries) error {
	return nil
}

func (e *colorEvents) Done(*ccm.WpColorManagerV1) error {
	e.done = true
	return nil
}

type outputEvents struct{ *colorEvents }

func (e outputEvents) Done(*cw.WlOutput) error {
	e.outputEvents = append(e.outputEvents, "done")
	return nil
}

func (e *colorEvents) ImageDescriptionChanged(*ccm.WpColorManagementOutputV1) error {
	e.outputChanged++
	e.outputEvents = append(e.outputEvents, "image_description_changed")
	return nil
}

func (e *colorEvents) PreferredChanged(*ccm.WpColorManagementSurfaceFeedbackV1, uint32) error {
	e.preferredChanged++
	return nil
}

func (e *colorEvents) PreferredChanged2(*ccm.WpColorManagementSurfaceFeedbackV1, uint32, uint32) error {
	e.preferredChanged++
	return nil
}

type descriptionEvents struct {
	readies int
	failed  string
}

func (e *descriptionEvents) Failed(_ *ccm.WpImageDescriptionV1, _ ccm.WpImageDescriptionV1Cause, msg string) error {
	e.failed = msg
	return nil
}

func (e *descriptionEvents) Ready(*ccm.WpImageDescriptionV1, uint32) error {
	e.readies++
	return nil
}

func (e *descriptionEvents) Ready2(*ccm.WpImageDescriptionV1, uint32, uint32) error {
	e.readies++
	return nil
}

type captured struct {
	size       image.Point
	stride     uint32
	bufferDone bool
	ready      bool
	failed     bool
}

func (f *captured) Buffer(_ *cwsc.ZwlrScreencopyFrameV1, _ cw.WlShmFormat, w, h, stride uint32) error {
	f.size, f.stride = image.Pt(int(w), int(h)), stride
	return nil
}

func (*captured) Flags(*cwsc.ZwlrScreencopyFrameV1, cwsc.ZwlrScreencopyFrameV1Flags) error {
	return nil
}

func (f *captured) Ready(*cwsc.ZwlrScreencopyFrameV1, uint32, uint32, uint32) error {
	f.ready = true
	return nil
}

func (f *captured) Failed(*cwsc.ZwlrScreencopyFrameV1) error {
	f.failed = true
	return nil
}

func (*captured) Damage(*cwsc.ZwlrScreencopyFrameV1, uint32, uint32, uint32, uint32) error {
	return nil
}

func (*captured) LinuxDmabuf(*cwsc.ZwlrScreencopyFrameV1, uint32, uint32, uint32) error {
	return nil
}

func (f *captured) BufferDone(*cwsc.ZwlrScreencopyFrameV1) error {
	f.bufferDone = true
	return nil
}

func pixelAt(t *testing.T, c *testclient.Client, p image.Point) [3]byte {
	t.Helper()
	mgr, err := cwsc.NewZwlrScreencopyManagerV1(c.Display, 3)
	if err != nil {
		t.Fatal(err)
	}
	c.Bind(mgr)
	f := &captured{}
	frame, err := mgr.CaptureOutput(f, 0, c.Outputs[0])
	if err != nil {
		t.Fatal(err)
	}
	c.DispatchUntil("the capture's buffer description", func() bool { return f.bufferDone })
	wl, pix := c.Target(f.size)
	if err := errors.Join(frame.Copy(wl), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	c.DispatchUntil("the capture", func() bool { return f.ready || f.failed })
	if f.failed {
		t.Fatal("the capture failed")
	}
	i := p.Y*int(f.stride) + p.X*4
	return [3]byte{pix[i+2], pix[i+1], pix[i]}
}

func iccProfile() []byte {
	head := make([]byte, 128)
	head[8] = 4
	copy(head[12:], "mntr")
	copy(head[16:], "RGB ")
	copy(head[20:], "XYZ ")
	copy(head[36:], "acsp")
	s15 := func(v float64) []byte { return binary.BigEndian.AppendUint32(nil, uint32(int32(math.Round(v*65536)))) }
	xyz := func(x, y, z float64) []byte {
		return slices.Concat([]byte("XYZ \x00\x00\x00\x00"), s15(x), s15(y), s15(z))
	}
	gamma := binary.BigEndian.AppendUint16([]byte("curv\x00\x00\x00\x00\x00\x00\x00\x01"), 563)
	tags := []struct {
		name string
		data []byte
	}{{"rXYZ", xyz(0.4360, 0.2225, 0.0139)}, {"gXYZ", xyz(0.3851, 0.7169, 0.0971)}, {"bXYZ", xyz(0.1431, 0.0606, 0.7139)}, {"rTRC", gamma}, {"gTRC", gamma}, {"bTRC", gamma}}
	table := binary.BigEndian.AppendUint32(nil, uint32(len(tags)))
	off := 128 + 4 + 12*len(tags)
	var body []byte
	for _, t := range tags {
		table = append(table, t.name...)
		table = binary.BigEndian.AppendUint32(table, uint32(off+len(body)))
		table = binary.BigEndian.AppendUint32(table, uint32(len(t.data)))
		body = append(body, t.data...)
	}
	return slices.Concat(head, table, body)
}

func awaitDescription(t *testing.T, c *testclient.Client, what string, ev *descriptionEvents) {
	t.Helper()
	c.DispatchUntil(what, func() bool { return ev.readies > 0 || ev.failed != "" })
	if ev.failed != "" {
		t.Fatalf("%s failed: %s", what, ev.failed)
	}
}

func TestUpgradeCarriesColorManagement(t *testing.T) {
	binary := buildLysWM(t)
	s := startProcess(t, binary, "color_management: true\nanimations:\n  enabled: false\n")
	c := testclient.Connect(t, s.waylandSocket())
	ev := &colorEvents{}
	m, err := ccm.NewWpColorManagerV1(c.Display, 3, ev)
	if err != nil {
		t.Fatal(err)
	}
	c.Bind(m)
	c.DispatchUntil("the colour manager's features", func() bool { return ev.done })

	params, err := m.CreateParametricCreator()
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(params.SetTfNamed(ccm.WpColorManagerV1TransferFunctionSt2084Pq), params.SetPrimariesNamed(ccm.WpColorManagerV1PrimariesBt2020)); err != nil {
		t.Fatal(err)
	}
	paramsEv := &descriptionEvents{}
	parametric, err := params.Create(paramsEv)
	if err != nil {
		t.Fatal(err)
	}
	awaitDescription(t, c, "the parametric description", paramsEv)

	profile := iccProfile()
	file, err := unix.MemfdCreate("icc", unix.MFD_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(file)
	if _, err := unix.Write(file, profile); err != nil {
		t.Fatal(err)
	}
	icc, err := m.CreateIccCreator()
	if err != nil {
		t.Fatal(err)
	}
	if err := icc.SetIccFile(file, 0, uint32(len(profile))); err != nil {
		t.Fatal(err)
	}
	iccEv := &descriptionEvents{}
	if _, err := icc.Create(iccEv); err != nil {
		t.Fatal(err)
	}
	awaitDescription(t, c, "the ICC description", iccEv)
	if err := unix.Ftruncate(file, 0); err != nil {
		t.Fatal(err)
	}

	const level = 0x94
	w := c.WindowWith("tagged", testclient.Fill(level, level, level), func(w *testclient.Window) error {
		tag, err := m.GetSurface(w.Surface)
		if err != nil {
			return err
		}
		return tag.SetImageDescription(parametric, ccm.WpColorManagerV1RenderIntentPerceptual)
	})
	c.DispatchUntil("the window to map", w.Mapped)
	c.DispatchUntil("the window's first frame on screen", func() bool { return w.Frames > 0 })
	wl, err := cw.NewWlOutput(c.Display, 4, outputEvents{ev})
	if err != nil {
		t.Fatal(err)
	}
	c.Bind(wl)
	if _, err := m.GetOutput(ev, wl); err != nil {
		t.Fatal(err)
	}
	if _, err := m.GetSurfaceFeedback(ev, w.Surface); err != nil {
		t.Fatal(err)
	}
	c.Sync()
	before := s.windows()
	if len(before) != 1 {
		t.Fatalf("windows %+v", before)
	}
	r := before[0].Rect
	centre := image.Pt(r.X+r.Width/2, r.Y+r.Height/2)
	shown := pixelAt(t, c, centre)
	if shown == [3]byte{level, level, level} {
		t.Fatalf("the tagged window shows its raw level %v: the tag changed nothing to compare across the upgrade", shown)
	}
	outputChanged, preferredChanged := ev.outputChanged, ev.preferredChanged
	ev.outputEvents = nil

	result, err := s.upgrade(buildCopy(t, binary))
	if err != nil {
		t.Fatalf("upgrade: %v\n%s", err, s.log())
	}
	if result.Clients != 1 || result.Windows != 1 {
		t.Fatalf("result %+v", result)
	}
	c.DispatchUntil("the new compositor's image_description_changed and preferred_changed", func() bool {
		return ev.outputChanged > outputChanged && ev.preferredChanged > preferredChanged
	})
	c.Sync()
	if ev.outputChanged != outputChanged+1 || ev.preferredChanged != preferredChanged+1 {
		t.Fatalf("after the upgrade %d image_description_changed and %d preferred_changed, want one each", ev.outputChanged-outputChanged, ev.preferredChanged-preferredChanged)
	}
	if i := slices.Index(ev.outputEvents, "image_description_changed"); i < 0 || !slices.Equal(ev.outputEvents[i:], []string{"image_description_changed", "done"}) {
		t.Fatalf("the output heard %v after the upgrade, want image_description_changed followed by one done", ev.outputEvents)
	}
	if paramsEv.readies != 1 || iccEv.readies != 1 {
		t.Fatalf("the descriptions heard %d and %d ready events across the upgrade, want one each: ready is sent once per object", paramsEv.readies, iccEv.readies)
	}
	if got := pixelAt(t, c, centre); got != shown {
		t.Fatalf("the new compositor shows the tagged window as %v, the old one %v", got, shown)
	}
	frames := w.Frames
	if err := w.Redraw(); err != nil {
		t.Fatal(err)
	}
	c.DispatchUntil("a frame from the new compositor", func() bool { return w.Frames > frames })
	if got := pixelAt(t, c, centre); got != shown {
		t.Fatalf("a commit after the upgrade shows the tagged window as %v, before it %v", got, shown)
	}
}
