package harness

import (
	"bufio"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
)

func TestSpawnedCommandsReportHowTheyExited(t *testing.T) {
	t.Parallel()
	r := &reports{}
	h := StartWith(t, func(cfg *compositor.Config) { cfg.Report = r.report }, small)
	h.dispatch("exec true")
	h.dispatch("exec exit 3")
	h.dispatch(`exec ulimit -c 0; kill -SEGV "$$"`)
	want := []string{
		`compositor: "exit 3" exited with status 3`,
		`compositor: "ulimit -c 0; kill -SEGV \"$$\"" was killed by signal SIGSEGV`,
	}
	var got []string
	h.Eventually("both failures reported", func(*headless.Backend, *compositor.Compositor) bool {
		for _, err := range r.take() {
			got = append(got, err.Error())
		}
		return len(got) >= len(want)
	})
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("reports %q, want %q", got, want)
	}
}

func TestSpawnedCommandsWriteToTheCompositorsStderr(t *testing.T) {
	t.Parallel()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = read.Close() })
	h := StartWith(t, func(cfg *compositor.Config) { cfg.ExecOutput.Stderr = write }, small)
	h.dispatch("exec echo spawned-to-stderr >&2")
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		c.WaitForExecs()
		return write.Close()
	})
	if line, err := bufio.NewReader(read).ReadString('\n'); strings.TrimSpace(line) != "spawned-to-stderr" {
		t.Fatalf("the file the compositor execs with as stderr got %q (%v)", line, err)
	}
}
