package spawn

import (
	"fmt"
	"io"
	"os"
	"syscall"
	"testing"

	"github.com/stubbedev/lys-lib/testhome"
	"golang.org/x/sys/unix"
)

const (
	helperEnv     = "LYS_WM_SPAWN_TEST_HELPER"
	helperCopy    = "copy"
	helperBlock   = "block"
	helperParent  = "parent"
	helperOutput  = "out"
	helperErrors  = "err"
	helperCopyEnd = 7
)

func TestMain(m *testing.M) {
	switch os.Getenv(helperEnv) {
	case helperCopy:
		copyThirdToFourth()
	case helperBlock:
		block()
	case helperParent:
		parentOfABlockedChild()
	}
	testhome.Run(m)
}

func TestTheTestsReachNoHostSession(t *testing.T) { testhome.RequireNoHostSession(t) }

func copyThirdToFourth() {
	if _, err := io.Copy(os.NewFile(4, "out"), os.NewFile(3, "in")); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Print(helperOutput)
	fmt.Fprint(os.Stderr, helperErrors)
	os.Exit(helperCopyEnd)
}

func block() {
	for {
		unix.Pause()
	}
}

func helper(mode string) Command {
	return Command{Path: os.Args[0], Args: []string{os.Args[0]}, Env: append(os.Environ(), helperEnv+"="+mode)}
}

func parentOfABlockedChild() {
	s, err := Start()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	c := helper(helperBlock)
	c.DeathSignal = syscall.SIGTERM
	p, err := s.Spawn(c)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("%d %d\n", s.process.Pid, p.Pid)
	block()
}
