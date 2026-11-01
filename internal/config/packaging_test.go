package config

import (
	"bufio"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const packagingDir = "../../packaging"

func readPackaging(t *testing.T, rel string) []string {
	t.Helper()
	f, err := os.Open(filepath.Join(packagingDir, rel))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var lines []string
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		lines = append(lines, scan.Text())
	}
	if err := scan.Err(); err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return lines
}

func debianRecommends(t *testing.T) []string {
	t.Helper()
	var names []string
	inField := false
	for _, line := range readPackaging(t, "debian/control.in") {
		if strings.HasPrefix(line, "Recommends:") {
			inField = true
			continue
		}
		if !inField {
			continue
		}
		if !strings.HasPrefix(line, " ") {
			break
		}
		names = append(names, strings.TrimSuffix(strings.TrimSpace(line), ","))
	}
	return names
}

func rpmRecommends(t *testing.T) []string {
	t.Helper()
	var names []string
	for _, line := range readPackaging(t, "rpm/lys-wm.spec.in") {
		if rest, ok := strings.CutPrefix(line, "Recommends:"); ok {
			names = append(names, strings.TrimSpace(rest))
		}
	}
	return names
}

func assertRecommended(t *testing.T, want string) {
	t.Helper()
	for format, names := range map[string][]string{"Debian": debianRecommends(t), "RPM": rpmRecommends(t)} {
		if !slices.Contains(names, want) {
			t.Errorf("the %s package recommends %v, without %q", format, names, want)
		}
	}
}

func TestPackagesRecommendEveryPortalBackendTheConfigNames(t *testing.T) {
	seen := map[string]bool{}
	for _, line := range readPackaging(t, "xdg-desktop-portal/lys-wm-portals.conf") {
		_, backends, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		for backend := range strings.SplitSeq(backends, ";") {
			seen[backend] = true
		}
	}
	if len(seen) == 0 {
		t.Fatal("lys-wm-portals.conf names no backends")
	}
	for backend := range seen {
		pkg := "xdg-desktop-portal-" + backend
		if backend == "lys-shell" {
			pkg = backend
		}
		assertRecommended(t, pkg)
	}
}
