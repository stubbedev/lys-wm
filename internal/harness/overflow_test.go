package harness

import (
	"testing"

	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/wm"
)

func TestABufferLargerThanItsFrameStaysInsideIt(t *testing.T) {
	onEachConfiguredRenderer(t, testABufferLargerThanItsFrameStaysInsideIt)
}

func testABufferLargerThanItsFrameStaysInsideIt(t *testing.T, start gpuStarter) {
	h := start(t, func(cfg *compositor.Config) {
		heldForever(cfg)
		cfg.WM.Gaps = wm.Gaps{Inner: 20}
	}, small)
	bg := [3]byte{compositor.Background.R, compositor.Background.G, compositor.Background.B}
	c, w := open(t, h, "big", fillGreen)
	h.Eventually("the window over the output", showing(150, 50, greenColor))
	w.Stop()
	w.Answer = testclient.AnswerKeepingTheBuffer
	_, _ = open(t, h, "right", fillBlue)
	c.DispatchUntil("the configure answered", func() bool { return w.Configures > 1 })
	c.Sync()
	h.Eventually("the change landed", landed)
	h.stillShows("the buffer inside its frame", 65, 50, greenColor)
	h.stillShows("not in the gap beside it", 75, 50, bg)
	h.stillShows("the neighbour's own", 95, 50, blueColor)
}
