package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDriverOfACard(t *testing.T) {
	root := t.TempDir()
	card := filepath.Join(root, "pci", "drm", "card0")
	if err := os.MkdirAll(card, 0o700); err != nil {
		t.Fatal(err)
	}
	d := UdevDevice{Devnode: "/dev/dri/card0", Syspath: card}
	if got, err := d.Driver(); err != nil || got != "" {
		t.Errorf("no driver bound = %q, %v, want nothing", got, err)
	}
	if err := os.Symlink("../../bus/pci/drivers/nvidia", filepath.Join(root, "pci", "driver")); err != nil {
		t.Fatal(err)
	}
	if got, err := d.Driver(); err != nil || got != "nvidia" {
		t.Errorf("driver = %q, %v, want nvidia", got, err)
	}
}
