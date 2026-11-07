package compositor

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"strconv"

	"github.com/stubbedev/lys-wm/internal/wm"
)

type Swallow struct {
	Terminals   *regexp.Regexp
	ExceptTitle *regexp.Regexp
	Proc        fs.ReadFileFS
}

const swallowDepth = 8

type procEntry struct {
	ppid   int32
	nsPIDs int
}

func readProc(proc fs.ReadFileFS, pid string) (procEntry, error) {
	data, err := proc.ReadFile(pid + "/status")
	if err != nil {
		return procEntry{}, fmt.Errorf("compositor: read the status of process %s: %w", pid, err)
	}
	var e procEntry
	found := false
	for line := range bytes.Lines(data) {
		key, value, ok := bytes.Cut(line, []byte(":"))
		if !ok {
			continue
		}
		switch string(key) {
		case "PPid":
			n, err := strconv.ParseInt(string(bytes.TrimSpace(value)), 10, 32)
			if err != nil {
				return procEntry{}, fmt.Errorf("compositor: parse the parent of process %s from %q: %w", pid, value, err)
			}
			e.ppid, found = int32(n), true
		case "NSpid":
			e.nsPIDs = len(bytes.Fields(value))
		}
	}
	if !found {
		return procEntry{}, fmt.Errorf("compositor: the status of process %s names no parent", pid)
	}
	return e, nil
}

func ancestors(proc fs.ReadFileFS, pid int32, nsDepth int, out *[swallowDepth]int32) (int, error) {
	e, err := readProc(proc, strconv.FormatInt(int64(pid), 10))
	if err != nil || e.nsPIDs != nsDepth {
		return 0, err
	}
	n := 0
	for e.ppid > 1 {
		out[n] = e.ppid
		n++
		if n == swallowDepth {
			return n, nil
		}
		if e, err = readProc(proc, strconv.FormatInt(int64(e.ppid), 10)); err != nil {
			return n, err
		}
	}
	return n, nil
}

func (c *Compositor) swallowTarget(t toplevel, float bool) wm.WindowID {
	cfg := c.swallow
	if cfg.Terminals == nil || float || t.parent() != nil || t.sandboxed() || t.pid() <= 0 || cfg.ExceptTitle != nil && cfg.ExceptTitle.MatchString(t.Title()) {
		return 0
	}
	if !c.procKnown {
		self, err := readProc(cfg.Proc, "self")
		if err != nil {
			c.report(fmt.Errorf("compositor: swallow: %w", err))
			return 0
		}
		c.procNSDepth, c.procKnown = self.nsPIDs, true
	}
	var line [swallowDepth]int32
	n, err := ancestors(cfg.Proc, t.pid(), c.procNSDepth, &line)
	if err != nil && n == 0 {
		return 0
	}
	for _, pid := range line[:n] {
		for _, w := range slices.Backward(c.windows) {
			if w.top.pid() == pid && cfg.Terminals.MatchString(w.top.AppID()) && c.wm.Swallowable(w.id) {
				return w.id
			}
		}
	}
	return 0
}

var procFS = func() fs.ReadFileFS {
	proc, ok := os.DirFS("/proc").(fs.ReadFileFS)
	if !ok {
		panic("compositor: os.DirFS no longer reads whole files")
	}
	return proc
}()

func swallowedBy(s wm.State, id wm.WindowID) wm.WindowID {
	by, _ := s.SwallowedBy(id)
	return by
}
