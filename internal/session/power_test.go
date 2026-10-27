package session

import (
	"os"
	"path/filepath"
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
