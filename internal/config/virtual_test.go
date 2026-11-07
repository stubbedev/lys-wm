package config

import (
	"image"
	"slices"
	"strings"
	"testing"

	"github.com/stubbedev/lys-wm/internal/compositor"
)

func TestVirtualOutputsParseAndRefuseWhatIsIncomplete(t *testing.T) {
	cfg, err := Parse("t.yaml", []byte("virtual_outputs:\n  - name: tablet\n    mode: 2048x1536@30\n  - name: VIRT-2\n    mode: 1280x720\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := []compositor.VirtualOutput{{Name: "tablet", Size: image.Pt(2048, 1536), RefreshMHz: 30000}, {Name: "VIRT-2", Size: image.Pt(1280, 720), RefreshMHz: 60000}}
	if !slices.Equal(cfg.VirtualOutputs, want) {
		t.Fatalf("virtual outputs %+v, want %+v", cfg.VirtualOutputs, want)
	}
	for text, problem := range map[string]string{
		"virtual_outputs:\n  - mode: 800x600\n":                                      "virtual_outputs[0].name: is missing",
		"virtual_outputs:\n  - name: a\n":                                            "virtual_outputs[0].mode: is missing",
		"virtual_outputs:\n  - name: a\n    mode: big\n":                             `"big" is not <width>x<height>`,
		"virtual_outputs:\n  - name: a\n    mode: 8x8\n  - name: a\n    mode: 9x9\n": `virtual_outputs[1].name: "a" names an earlier virtual output too`,
	} {
		if _, err := Parse("t.yaml", []byte(text)); err == nil || !strings.Contains(err.Error(), problem) {
			t.Errorf("%q: %v, want %q", text, err, problem)
		}
	}
}
