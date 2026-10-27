package session

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func OnBattery(roots Roots) (bool, error) {
	dir := filepath.Join(roots.Sys, "class", string(PowerSupply))
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("session: list %s: %w", dir, err)
	}
	external := false
	for _, e := range entries {
		kind, err := attribute(dir, e.Name(), "type")
		if err != nil {
			return false, err
		}
		if kind != "Mains" && kind != "USB" {
			continue
		}
		external = true
		online, err := attribute(dir, e.Name(), "online")
		if err != nil {
			return false, err
		}
		if online == "1" {
			return false, nil
		}
	}
	return external, nil
}

func attribute(dir, supply, name string) (string, error) {
	b, err := os.ReadFile(filepath.Join(dir, supply, name)) //nolint:gosec // a sysfs attribute of a supply the kernel listed
	if err != nil {
		return "", fmt.Errorf("session: read power supply %s: %w", supply, err)
	}
	return strings.TrimSpace(string(b)), nil
}
