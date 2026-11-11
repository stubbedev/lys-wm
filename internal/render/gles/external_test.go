package gles

import (
	"testing"

	"github.com/stubbedev/lys-lib/fourcc"

	"github.com/stubbedev/lys-wm/internal/gpu"
)

func TestABufferNamingNoModifierIsSampledTheWayLinearIs(t *testing.T) {
	const tiled = fourcc.Modifier(7)
	for _, tc := range []struct {
		name     string
		listed   []gpu.ModifierSupport
		modifier fourcc.Modifier
		external bool
	}{
		{"linear only through the external target", []gpu.ModifierSupport{{Modifier: tiled}, {Modifier: fourcc.Linear, ExternalOnly: true}}, fourcc.Invalid, true},
		{"linear as a 2D texture", []gpu.ModifierSupport{{Modifier: tiled, ExternalOnly: true}, {Modifier: fourcc.Linear}}, fourcc.Invalid, false},
		{"no linear listed", []gpu.ModifierSupport{{Modifier: tiled, ExternalOnly: true}}, fourcc.Invalid, false},
		{"no modifier listed", nil, fourcc.Invalid, false},
		{"a named modifier keeps its own answer", []gpu.ModifierSupport{{Modifier: tiled}, {Modifier: fourcc.Linear, ExternalOnly: true}}, tiled, false},
		{"linear named", []gpu.ModifierSupport{{Modifier: tiled}, {Modifier: fourcc.Linear, ExternalOnly: true}}, fourcc.Linear, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &Renderer{modifiers: map[fourcc.Format][]gpu.ModifierSupport{fourcc.XRGB8888: tc.listed}}
			external, err := r.externalOnly(gpu.DMABUF{Format: fourcc.XRGB8888, Modifier: tc.modifier})
			if err != nil {
				t.Fatal(err)
			}
			if external != tc.external {
				t.Fatalf("a buffer with modifier %v among %+v is external-only %v, want %v", tc.modifier, tc.listed, external, tc.external)
			}
		})
	}
}
