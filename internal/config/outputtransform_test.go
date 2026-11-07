package config

import (
	"strings"
	"testing"

	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/wm"
)

func TestOutputsTakeATransformAMirrorAndReservedEdges(t *testing.T) {
	cfg, err := Parse("t.yaml", []byte("outputs:\n  - match: DP-1\n    transform: flipped-270\n    reserved: [10, 0, 20, 5]\n  - match: HDMI-A-1\n    mirror: 'desc:Dell U2720Q'\n"))
	if err != nil {
		t.Fatal(err)
	}
	if r := cfg.Outputs.Named[0]; r.Transform != render.Flipped270 || r.Reserved != (compositor.Insets{Top: 10, Bottom: 20, Left: 5}) || r.Mirror != nil {
		t.Errorf("the first rule %+v", r)
	}
	if r := cfg.Outputs.Named[1]; r.Mirror == nil || *r.Mirror != (wm.OutputMatch{Description: "Dell U2720Q"}) || r.Transform != render.Normal {
		t.Errorf("the mirror rule %+v", r)
	}
	for text, problem := range map[string]string{
		"outputs:\n  - match: DP-1\n    transform: 45\n":           `"45" is not one of normal, 90`,
		"outputs:\n  - match: DP-1\n    reserved: [1, 2, 3]\n":     "needs four numbers",
		"outputs:\n  - match: DP-1\n    reserved: [1, 2, 3, -4]\n": "none below 0",
		"outputs:\n  - match: DP-1\n    mirror: '*'\n":             "needs a connector name",
	} {
		if _, err := Parse("t.yaml", []byte(text)); err == nil || !strings.Contains(err.Error(), problem) {
			t.Errorf("%q: %v, want %q", text, err, problem)
		}
	}
}
