package compositor

import (
	"strconv"
	"testing"
	"testing/fstest"
)

type countingProc struct {
	fstest.MapFS
	reads int
}

func (p *countingProc) ReadFile(name string) ([]byte, error) {
	p.reads++
	return p.MapFS.ReadFile(name)
}

func status(ppid, nspid string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte("Name:\tx\nPPid:\t" + ppid + "\nNSpid:\t" + nspid + "\n")}
}

func TestTheAncestryWalkReadsOneFilePerLevelUpToItsBound(t *testing.T) {
	proc := &countingProc{MapFS: fstest.MapFS{
		"30/status": status("20", "30"),
		"20/status": status("10", "20"),
		"10/status": status("1", "10"),
	}}
	var line [swallowDepth]int32
	n, err := ancestors(proc, 30, 1, &line)
	if err != nil || n != 2 || line[0] != 20 || line[1] != 10 {
		t.Fatalf("ancestors %v (%d), %v", line[:n], n, err)
	}
	if proc.reads != 3 {
		t.Fatalf("the walk read %d files, want one per process", proc.reads)
	}

	deep := &countingProc{MapFS: fstest.MapFS{}}
	for pid := 100; pid < 120; pid++ {
		deep.MapFS[strconv.Itoa(pid)+"/status"] = status(strconv.Itoa(pid+1), strconv.Itoa(pid))
	}
	if n, err := ancestors(deep, 100, 1, &line); err != nil || n != swallowDepth || deep.reads != swallowDepth {
		t.Fatalf("a deep line gave %d ancestors in %d reads, %v; want the bound %d", n, deep.reads, err, swallowDepth)
	}

	nested := &countingProc{MapFS: fstest.MapFS{"40/status": status("20", "40 2")}}
	if n, err := ancestors(nested, 40, 1, &line); err != nil || n != 0 || nested.reads != 1 {
		t.Fatalf("a process in another pid namespace gave %d ancestors in %d reads, %v", n, nested.reads, err)
	}
}
