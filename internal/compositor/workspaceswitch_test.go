package compositor

import (
	"image"
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/anim"
)

func TestWorkspaceSwitchStylesHalfway(t *testing.T) {
	area := image.Rect(0, 0, 1000, 600)
	c, err := anim.Linear.Timed(time.Second, true)
	if err != nil {
		t.Fatal(err)
	}
	mid := 500 * time.Millisecond
	cases := []struct {
		style         anim.Style
		forward       bool
		incoming, out view
	}{
		{anim.Style{Kind: anim.Slide}, true, view{offset: image.Pt(500, 0), scale: 1, alpha: 1}, view{offset: image.Pt(-500, 0), scale: 1, alpha: 1}},
		{anim.Style{Kind: anim.Slide}, false, view{offset: image.Pt(-500, 0), scale: 1, alpha: 1}, view{offset: image.Pt(500, 0), scale: 1, alpha: 1}},
		{anim.Style{Kind: anim.SlideVert}, true, view{offset: image.Pt(0, 300), scale: 1, alpha: 1}, view{offset: image.Pt(0, -300), scale: 1, alpha: 1}},
		{anim.Style{Kind: anim.FadeStyle}, true, view{scale: 1, alpha: 0.5}, view{scale: 1, alpha: 0.5}},
		{anim.Style{Kind: anim.Popin, Percent: 60}, true, view{scale: 0.8, alpha: 0.5}, view{scale: 1, alpha: 0.5}},
	}
	for _, tc := range cases {
		sw := &workspaceSwitch{forward: tc.forward, style: tc.style, progress: anim.NewTween(0.0, anim.Lerp)}
		sw.progress.Set(1, 0, c)
		if got := sw.view(area, true, mid); got != tc.incoming {
			t.Errorf("%v forward %v: incoming %+v, want %+v", tc.style, tc.forward, got, tc.incoming)
		}
		if got := sw.view(area, false, mid); got != tc.out {
			t.Errorf("%v forward %v: outgoing %+v, want %+v", tc.style, tc.forward, got, tc.out)
		}
	}
	if got := (view{scale: 0.5, alpha: 1}).rect(image.Rect(0, 0, 1000, 600), area); got != image.Rect(250, 150, 750, 450) {
		t.Errorf("a half-scale view puts the area at %v", got)
	}
}
