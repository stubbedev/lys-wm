package xwayland

import (
	"fmt"
	"os"
	"testing"

	"github.com/stubbedev/lys-lib/testhome"
)

const (
	fakeXwaylandEnv    = "LYS_WM_XWAYLAND_TEST_FAKE"
	fakeXwaylandSays   = "parent "
	fakeXwaylandStatus = 3
)

func TestMain(m *testing.M) {
	if os.Getenv(fakeXwaylandEnv) != "" {
		fmt.Fprintf(os.Stderr, "%s%d\n", fakeXwaylandSays, os.Getppid())
		os.Exit(fakeXwaylandStatus)
	}
	testhome.Run(m)
}

func TestTheTestsReachNoHostSession(t *testing.T) { testhome.RequireNoHostSession(t) }
