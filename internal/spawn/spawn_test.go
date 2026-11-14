package spawn

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/rtkit/rtkittest"
)

const (
	loweredEnv = "LYS_WM_SPAWN_TEST_LOWERED"
	exitBound  = 30 * time.Second
)

func started(t *testing.T) *Spawner {
	t.Helper()
	s, err := Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s
}

func inLoweredChild(t *testing.T) bool {
	t.Helper()
	if os.Getenv(loweredEnv) != "" {
		return true
	}
	cmd := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$", "-test.count=1", "-test.v")
	cmd.Env = append(os.Environ(), loweredEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the child that lowers its hard RLIMIT_RTTIME: %v\n%s", err, out)
	}
	if _, reason, skipped := strings.Cut(string(out), "--- SKIP: "+t.Name()); skipped {
		t.Skip(strings.TrimSpace(reason))
	}
	if !strings.Contains(string(out), "--- PASS: "+t.Name()) {
		t.Fatalf("the child that lowers its hard RLIMIT_RTTIME did not run %s:\n%s", t.Name(), out)
	}
	return false
}

func hardRTTime(t *testing.T, pid int) string {
	t.Helper()
	limits, err := os.ReadFile(fmt.Sprintf("/proc/%d/limits", pid))
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.SplitSeq(string(limits), "\n") {
		if rest, ok := strings.CutPrefix(line, "Max realtime timeout"); ok {
			if fields := strings.Fields(rest); len(fields) >= 2 {
				return fields[1]
			}
		}
	}
	t.Fatalf("no realtime timeout in /proc/%d/limits:\n%s", pid, limits)
	return ""
}

func limitString(v uint64) string {
	if v == unix.RLIM_INFINITY {
		return "unlimited"
	}
	return strconv.FormatUint(v, 10)
}

func TestAChildGetsTheLimitsTheSpawnerStartedWithItsFilesItsEnvironmentAndItsExit(t *testing.T) {
	if !inLoweredChild(t) {
		return
	}
	var original unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_RTTIME, &original); err != nil {
		t.Fatal(err)
	}
	if original.Max <= rtkittest.RTTimeUSecMax {
		t.Skipf("the hard RLIMIT_RTTIME is %d µs already, no more than rtkit's %d", original.Max, rtkittest.RTTimeUSecMax)
	}
	s := started(t)
	lowered := unix.Rlimit{Cur: rtkittest.RTTimeUSecMax, Max: rtkittest.RTTimeUSecMax}
	if err := unix.Setrlimit(unix.RLIMIT_RTTIME, &lowered); err != nil {
		t.Fatalf("lower RLIMIT_RTTIME to %+v as rtkit has lys-wm do: %v", lowered, err)
	}
	if got := hardRTTime(t, os.Getpid()); got != limitString(rtkittest.RTTimeUSecMax) {
		t.Fatalf("this process's hard RLIMIT_RTTIME is %s after lowering it", got)
	}
	in, toChild, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	fromChild, out, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	c := helper(helperCopy)
	c.Stdout, c.Stderr, c.ExtraFiles = &stdout, &stdout, []*os.File{in, out}
	p, err := s.Spawn(c)
	in.Close()
	out.Close()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := hardRTTime(t, p.Pid), limitString(original.Max); got != want {
		t.Fatalf("the spawned child's hard RLIMIT_RTTIME is %s, want the %s the spawner started with", got, want)
	}
	if _, err := toChild.WriteString("through the spawner"); err != nil {
		t.Fatal(err)
	}
	toChild.Close()
	copied, err := io.ReadAll(fromChild)
	fromChild.Close()
	if err != nil {
		t.Fatal(err)
	}
	if string(copied) != "through the spawner" {
		t.Fatalf("the child copied %q from its fd 3 to its fd 4", copied)
	}
	exit, ok := errors.AsType[*ExitError](p.Wait())
	if !ok || !exit.Status.Exited() || exit.Status.ExitStatus() != helperCopyEnd {
		t.Fatalf("the child ended with %v, want exit status %d", exit, helperCopyEnd)
	}
	if want := helperOutput + helperErrors; stdout.String() != want {
		t.Fatalf("the child's stdout and stderr reached their one writer as %q, want %q", stdout.String(), want)
	}
}

func TestAChildKilledBySignalSaysSoAndIsDoneAfterItsWait(t *testing.T) {
	s := started(t)
	p, err := s.Spawn(helper(helperBlock))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	exit, ok := errors.AsType[*ExitError](p.Wait())
	if !ok || !exit.Status.Signaled() || exit.Status.Signal() != syscall.SIGKILL {
		t.Fatalf("the killed child ended with %v, want SIGKILL", exit)
	}
	if err := p.Signal(syscall.SIGKILL); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("a signal after the wait returned %v, want os.ErrProcessDone", err)
	}
}

func TestASpawnThatCannotStartFailsAndTheSpawnerCarriesOn(t *testing.T) {
	s := started(t)
	missing := "/nonexistent/lys-wm-spawn-test"
	if _, err := s.Spawn(Command{Path: missing, Args: []string{missing}}); err == nil || !strings.Contains(err.Error(), missing) {
		t.Fatalf("spawning %s returned %v, want an error naming it", missing, err)
	}
	p, err := s.Spawn(Command{Path: os.Args[0], Args: []string{os.Args[0], "-test.run=^$"}, Env: os.Environ()})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestALostSpawnerFailsEverySpawnAndWait(t *testing.T) {
	s, err := Start()
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Spawn(helper(helperBlock))
	if err != nil {
		t.Fatal(err)
	}
	child, err := unix.PidfdOpen(p.Pid, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(child)
	if err := s.process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := p.Wait(); err == nil {
		t.Fatal("a child's wait succeeded after its spawner was killed")
	}
	if _, err := s.Spawn(helper(helperBlock)); err == nil {
		t.Fatal("a spawn succeeded after the spawner was killed")
	}
	if err := s.Close(); err == nil || !strings.Contains(err.Error(), "killed") {
		t.Fatalf("closing a killed spawner returned %v, want how it ended", err)
	}
	if err := unix.PidfdSendSignal(child, syscall.SIGKILL, nil, 0); err != nil && !errors.Is(err, unix.ESRCH) {
		t.Fatal(err)
	}
	waitExit(t, child, "the blocked child of the killed spawner")
}

func waitExit(t *testing.T, pidfd int, what string) {
	t.Helper()
	fds := []unix.PollFd{{Fd: int32(pidfd), Events: unix.POLLIN}}
	for {
		n, err := unix.Poll(fds, int(exitBound.Milliseconds()))
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			t.Fatalf("poll the pidfd of %s: %v", what, err)
		}
		if n == 0 {
			t.Fatalf("%s still runs %v later", what, exitBound)
		}
		return
	}
}

func TestClosingTheSpawnerEndsItAndTheChildrenThatAskedForADeathSignal(t *testing.T) {
	s, err := Start()
	if err != nil {
		t.Fatal(err)
	}
	c := helper(helperBlock)
	c.DeathSignal = syscall.SIGTERM
	p, err := s.Spawn(c)
	if err != nil {
		t.Fatal(err)
	}
	child, err := unix.PidfdOpen(p.Pid, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(child)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	waitExit(t, child, "the child that asked for SIGTERM when its spawner dies")
}

func TestTheSpawnerAndTheChildrenThatAskedForADeathSignalEndWithTheirParent(t *testing.T) {
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), helperEnv+"="+helperParent)
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("read the spawner's and its child's pids: %v (%v)", err, cmd.Wait())
	}
	var spawner, child int
	if _, err := fmt.Sscanf(line, "%d %d", &spawner, &child); err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("parse %q: %v (%v)", line, err, cmd.Wait())
	}
	var pidfds []int
	for _, pid := range []int{spawner, child} {
		fd, err := unix.PidfdOpen(pid, 0)
		if err != nil {
			_ = cmd.Process.Kill()
			t.Fatalf("pidfd for %d: %v (%v)", pid, err, cmd.Wait())
		}
		defer unix.Close(fd)
		pidfds = append(pidfds, fd)
	}
	if comm, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", spawner)); err != nil || strings.TrimSpace(string(comm)) != spawnerName {
		t.Errorf("the spawner is named %q (%v), want %q", comm, err, spawnerName)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("the killed parent exited cleanly")
	}
	waitExit(t, pidfds[0], "the spawner of the killed parent")
	waitExit(t, pidfds[1], "the child that asked for SIGTERM when its spawner dies")
}

func TestASignalToAChildThatHasExitedButIsNotWaitedForSaysItIsDone(t *testing.T) {
	s := started(t)
	p, err := s.Spawn(Command{Path: os.Args[0], Args: []string{os.Args[0], "-test.run=^$"}, Env: os.Environ()})
	if err != nil {
		t.Fatal(err)
	}
	waitExit(t, p.pidfd, "the child that runs no tests")
	if err := p.Signal(syscall.SIGKILL); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("a signal to an exited child returned %v, want os.ErrProcessDone", err)
	}
	if err := p.Wait(); err != nil {
		t.Fatalf("the child that was not killed ended with %v", err)
	}
}
