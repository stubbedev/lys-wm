package session

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func (d UdevDevice) BootVGA() (bool, error) {
	b, err := os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(d.Syspath)), "boot_vga"))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("session: boot_vga of %s: %w", d.Devnode, err)
	}
	return strings.TrimSpace(string(b)) == "1", nil
}

func (d UdevDevice) HasRenderNode() (bool, error) {
	nodes, err := filepath.Glob(filepath.Join(filepath.Dir(d.Syspath), "renderD*"))
	if err != nil {
		return false, fmt.Errorf("session: render nodes beside %s: %w", d.Devnode, err)
	}
	return len(nodes) > 0, nil
}

// Driver names the kernel driver bound to the card, "" for a card without
// one.
func (d UdevDevice) Driver() (string, error) {
	target, err := os.Readlink(filepath.Join(filepath.Dir(filepath.Dir(d.Syspath)), "driver"))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("session: driver of %s: %w", d.Devnode, err)
	}
	return filepath.Base(target), nil
}
