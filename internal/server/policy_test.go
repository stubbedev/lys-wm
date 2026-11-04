package server

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stubbedev/lys-wm/internal/wire"
)

var (
	interfaceBlock = regexp.MustCompile(`(?ms)= &wire\.Interface\{\n\tName:\s+"([a-z0-9_]+)",\n.*?\n\}\n`)
	section        = regexp.MustCompile(`(?ms)^\t(Requests|Events): \[\]wire\.MessageSpec\{\n(.*?)^\t\},`)
	message        = regexp.MustCompile(`(?m)^\t\t\{Name: "([a-z0-9_]+)"(.*)\},$`)
)

func generatedInterfaces(t *testing.T) []*wire.Interface {
	t.Helper()
	files, err := filepath.Glob("../protocol/server/*/*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("the generated protocols: %v", err)
	}
	var out []*wire.Interface
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, block := range interfaceBlock.FindAllSubmatch(data, -1) {
			iface := &wire.Interface{Name: string(block[1])}
			for _, sec := range section.FindAllSubmatch(block[0], -1) {
				var specs []wire.MessageSpec
				for _, m := range message.FindAllSubmatch(sec[2], -1) {
					spec := wire.MessageSpec{Name: string(m[1]), Destructor: strings.Contains(string(m[2]), "Destructor: true")}
					if strings.Contains(string(m[2]), "wire.ArgNewID") {
						spec.Args = []wire.Arg{{Type: wire.ArgNewID}}
					}
					specs = append(specs, spec)
				}
				if string(sec[1]) == "Requests" {
					iface.Requests = specs
				} else {
					iface.Events = specs
				}
			}
			out = append(out, iface)
		}
	}
	slices.SortFunc(out, func(a, b *wire.Interface) int { return strings.Compare(a.Name, b.Name) })
	return out
}

func classNames() map[requestClass]string {
	return map[requestClass]string{
		requestLatest: "latest", requestLatestKeyed: "latest-keyed", requestAppend: "append", requestCount: "count",
		requestDropped: "dropped", requestCreate: "create", requestDestructor: "destructor",
	}
}

func objectClassNames() map[objectClass]string {
	return map[objectClass]string{classJournal: "journal", classRefused: "refused", classInert: "inert", classGone: "gone"}
}

func TestEveryRequestHasAReviewedHandoverPolicy(t *testing.T) {
	var lines []string
	for _, iface := range generatedInterfaces(t) {
		p := policyOf(iface)
		lines = append(lines, fmt.Sprintf("%s = %s retire=%s", iface.Name, objectClassNames()[p.class], strings.Join(p.retire, ",")))
		for i, spec := range iface.Requests {
			lines = append(lines, fmt.Sprintf("%s.%s = %s", iface.Name, spec.Name, classNames()[p.requests[i]]))
		}
	}
	got := strings.Join(lines, "\n") + "\n"
	path := filepath.Join("testdata", "handover-policy.txt")
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("the handover policy of a request changed, or a new protocol has none. Decide what a new compositor needs to rebuild each one, then update %s:\n%s", path, firstDifference(string(want), got))
	}
}

func firstDifference(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := range min(len(w), len(g)) {
		if w[i] != g[i] {
			return fmt.Sprintf("line %d: reviewed %q, now %q", i+1, w[i], g[i])
		}
	}
	return fmt.Sprintf("reviewed %d lines, now %d", len(w), len(g))
}

func TestEveryNameInTheHandoverPolicyExists(t *testing.T) {
	known := map[string]*wire.Interface{}
	for _, iface := range generatedInterfaces(t) {
		known[iface.Name] = iface
	}
	hasMessage := func(specs []wire.MessageSpec, name string) bool {
		return slices.ContainsFunc(specs, func(s wire.MessageSpec) bool { return s.Name == name })
	}
	for _, table := range []map[string][]string{dropped, appended, keyed, counted} {
		for name, requests := range table {
			iface, ok := known[name]
			if !ok {
				t.Errorf("no interface %s", name)
				continue
			}
			for _, r := range requests {
				if !hasMessage(iface.Requests, r) {
					t.Errorf("%s has no request %s", name, r)
				}
			}
		}
	}
	for _, table := range []map[string][]string{retired, gone, retireAlways, dropEvents} {
		for name, events := range table {
			iface, ok := known[name]
			if !ok {
				t.Errorf("no interface %s", name)
				continue
			}
			for _, e := range events {
				if !hasMessage(iface.Events, e) {
					t.Errorf("%s has no event %s", name, e)
				}
			}
		}
	}
	for name := range refused {
		if _, ok := known[name]; !ok {
			t.Errorf("no interface %s", name)
		}
	}
	for name := range retired {
		if _, ok := inertInterfaces[name]; !ok {
			t.Errorf("%s is retired, but a new compositor cannot hold it inert", name)
		}
	}
}
