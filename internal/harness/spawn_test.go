package harness

import (
	"bufio"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
)

func TestSpawnedCommandsReportHowTheyExited(t *testing.T) {
	r := &reports{}
	h := StartWith(t, func(cfg *compositor.Config) { cfg.Report = r.report }, small)
	h.dispatch("exec true")
	h.dispatch("exec exit 3")
	h.dispatch(`exec kill -SEGV "$$"`)
	want := []string{
		`compositor: "exit 3" exited with status 3`,
		`compositor: "kill -SEGV \"$$\"" was killed by signal SIGSEGV`,
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
	h := Start(t, small)
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = read.Close() })
	var stderr *os.File
	h.Do(func(*headless.Backend, *compositor.Compositor) error {
		stderr, os.Stderr = os.Stderr, write
		return nil
	})
	h.dispatch("exec echo spawned-to-stderr >&2")
	h.Do(func(*headless.Backend, *compositor.Compositor) error {
		os.Stderr = stderr
		return write.Close()
	})
	lines := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(read).ReadString('\n')
		lines <- line
	}()
	select {
	case line := <-lines:
		if strings.TrimSpace(line) != "spawned-to-stderr" {
			t.Fatalf("the compositor's stderr got %q", line)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the spawned command's stderr never reached the compositor's")
	}
}
