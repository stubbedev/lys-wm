package harness

import (
	"errors"
	"testing"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	chc "github.com/stubbedev/lys-wm/internal/protocol/client/hyprlandctmcontrol"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/wire"
)

type ctmEvents struct{ blocked bool }

func (e *ctmEvents) Blocked(*chc.HyprlandCtmControlManagerV1) error {
	e.blocked = true
	return nil
}

func ctmControl(t *testing.T, h *Harness) (*testclient.Client, *chc.HyprlandCtmControlManagerV1, *ctmEvents) {
	t.Helper()
	c := testclient.Connect(t, h.Socket())
	ev := &ctmEvents{}
	m, err := chc.NewHyprlandCtmControlManagerV1(c.Display, 2, ev)
	if err != nil {
		t.Fatal(err)
	}
	c.Bind(m)
	if err := c.Display.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	return c, m, ev
}

func setCTM(t *testing.T, c *testclient.Client, m *chc.HyprlandCtmControlManagerV1, i int, mat backend.Matrix) {
	t.Helper()
	var f [9]wire.Fixed
	for j, v := range mat {
		f[j] = wire.FixedFromFloat(v)
	}
	if err := m.SetCtmForOutput(c.Outputs[i], f[0], f[1], f[2], f[3], f[4], f[5], f[6], f[7], f[8]); err != nil {
		t.Fatal(err)
	}
}

func outputCTMs(want ...backend.Matrix) func(*headless.Backend, *compositor.Compositor) bool {
	return func(b *headless.Backend, _ *compositor.Compositor) bool {
		for i, o := range b.Outputs() {
			if o.ColorTransform() != want[i] {
				return false
			}
		}
		return true
	}
}

func enableCTMs(h *Harness) {
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		for _, o := range b.Outputs() {
			o.EnableColorTransform()
		}
		return nil
	})
}

func TestCTMControlSetsOutputsOnCommitAndResetsThemWhenItGoes(t *testing.T) {
	h := Start(t, small, small)
	enableCTMs(h)
	warm := backend.Matrix{1, 0, 0, 0, 0.75, 0, 0, 0, 0.5}
	c, m, ev := ctmControl(t, h)
	if ev.blocked {
		t.Fatal("the first manager was blocked")
	}

	setCTM(t, c, m, 0, warm)
	setCTM(t, c, m, 1, warm)
	if err := c.Display.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	h.Eventually("no matrix before the commit", outputCTMs(backend.Identity, backend.Identity))
	if err := errors.Join(m.Commit(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	h.Eventually("the matrix on both outputs", outputCTMs(warm, warm))

	setCTM(t, c, m, 1, warm)
	if err := errors.Join(m.Commit(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	h.Eventually("the identity on the output the commit left out", outputCTMs(backend.Identity, warm))

	if err := errors.Join(m.Destroy(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	h.Eventually("the identity once the manager is gone", outputCTMs(backend.Identity, backend.Identity))
}

func TestASecondCTMManagerIsBlocked(t *testing.T) {
	h := Start(t, small)
	enableCTMs(h)
	warm := backend.Matrix{1, 0, 0, 0, 0.75, 0, 0, 0, 0.5}
	c, m, _ := ctmControl(t, h)
	setCTM(t, c, m, 0, warm)
	if err := errors.Join(m.Commit(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	h.Eventually("the first manager's matrix", outputCTMs(warm))

	other, om, ev := ctmControl(t, h)
	if !ev.blocked {
		t.Fatal("a second manager was not blocked")
	}
	setCTM(t, other, om, 0, backend.Matrix{0.5, 0, 0, 0, 0.5, 0, 0, 0, 0.5})
	if err := errors.Join(om.Commit(), other.Display.Roundtrip()); err != nil {
		t.Fatal(err)
	}
	h.Eventually("the first manager's matrix, the blocked one ignored", outputCTMs(warm))
}

func TestANegativeCTMEntryIsAProtocolError(t *testing.T) {
	h := Start(t, small)
	enableCTMs(h)
	c, m, _ := ctmControl(t, h)
	setCTM(t, c, m, 0, backend.Matrix{1, 0, 0, 0, -1, 0, 0, 0, 1})
	c.ExpectProtocolError(uint32(chc.HyprlandCtmControlManagerV1ErrorInvalidMatrix))
}
