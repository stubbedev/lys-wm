package drm

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTheRenderNodeComesFromTheCardsSysfsEntry(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "226:1", "device", "drm")
	for _, name := range []string{"card1", "renderD129"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "226:0", "device", "drm", "card0"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := renderNodeOf(root, 226, 1); err != nil || got != "/dev/dri/renderD129" {
		t.Fatalf("the card with a render node gave %q, %v", got, err)
	}
	if got, err := renderNodeOf(root, 226, 0); err != nil || got != "" {
		t.Fatalf("a display-only card gave %q, %v", got, err)
	}
}
