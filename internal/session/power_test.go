package session

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func supplies(t *testing.T, kinds map[string][2]string) Roots {
	t.Helper()
	root := t.TempDir()
	for name, attrs := range kinds {
		dir := filepath.Join(root, "class", "power_supply", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for file, v := range map[string]string{"type": attrs[0], "online": attrs[1]} {
			if v == "" {
				continue
			}
			if err := os.WriteFile(filepath.Join(dir, file), []byte(v+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	return Roots{Sys: root}
}

func TestOnBattery(t *testing.T) {
	for name, c := range map[string]struct {
		supplies map[string][2]string
		want     bool
	}{
		"no power supplies":            {nil, false},
		"a battery and no charger":     {map[string][2]string{"BAT0": {"Battery", ""}}, false},
		"mains unplugged":              {map[string][2]string{"BAT0": {"Battery", ""}, "AC": {"Mains", "0"}}, true},
		"mains plugged":                {map[string][2]string{"BAT0": {"Battery", ""}, "AC": {"Mains", "1"}}, false},
		"only a USB-C charger plugged": {map[string][2]string{"AC": {"Mains", "0"}, "ucsi-source-psy-1": {"USB", "1"}}, false},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := OnBattery(supplies(t, c.supplies))
			if err != nil || got != c.want {
				t.Fatalf("OnBattery = %v, %v; want %v", got, err, c.want)
			}
		})
	}
}

type vanishing struct {
	fs.FS
	gone string
}

func (v vanishing) ReadDir(name string) ([]fs.DirEntry, error) {
	entries, err := fs.ReadDir(v.FS, name)
	if err != nil {
		return nil, err
	}
	return entries, os.RemoveAll(v.gone)
}

func TestASupplyThatVanishesWhileItIsReadIsSkipped(t *testing.T) {
	roots := supplies(t, map[string][2]string{"BAT0": {"Battery", ""}, "AC": {"Mains", "0"}, "ucsi-source-psy-1": {"USB", "1"}})
	sys := vanishing{FS: os.DirFS(roots.Sys), gone: filepath.Join(roots.Sys, "class", "power_supply", "ucsi-source-psy-1")}
	got, err := onBattery(sys)
	if err != nil || !got {
		t.Fatalf("onBattery = %v, %v; want on battery once the plugged USB-C charger is gone", got, err)
	}
}

func TestAPowerSupplyReadErrorIsReturned(t *testing.T) {
	roots := supplies(t, map[string][2]string{"AC": {"Mains", ""}})
	if err := os.Mkdir(filepath.Join(roots.Sys, "class", "power_supply", "AC", "online"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := OnBattery(roots)
	if !errors.Is(err, syscall.EISDIR) || !strings.Contains(err.Error(), roots.Sys) || !strings.Contains(err.Error(), "AC/online") {
		t.Fatalf("OnBattery error %v, want EISDIR naming %s and AC/online", err, roots.Sys)
	}
}
