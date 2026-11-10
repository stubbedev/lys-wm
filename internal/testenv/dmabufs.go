package testenv

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const descriptors = "/proc/self/fd"

func OpenDMABUFs(t testing.TB) int {
	t.Helper()
	entries, err := os.ReadDir(descriptors)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		link, err := os.Readlink(filepath.Join(descriptors, e.Name()))
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			t.Fatal(err)
		case strings.HasPrefix(link, "/dmabuf:"):
			n++
		}
	}
	return n
}
