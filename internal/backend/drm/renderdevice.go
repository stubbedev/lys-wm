package drm

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"

	kdrm "github.com/stubbedev/lys-lib/drm"
	"golang.org/x/sys/unix"
)

const sysDevChar = "/sys/dev/char"

func renderNodeOf(root string, major, minor uint32) (string, error) {
	nodes, err := filepath.Glob(filepath.Join(root, fmt.Sprintf("%d:%d", major, minor), "device", "drm", "renderD*"))
	if err != nil {
		return "", fmt.Errorf("look for the render node of DRM device %d:%d: %w", major, minor, err)
	}
	if len(nodes) == 0 {
		return "", nil
	}
	slices.Sort(nodes)
	return filepath.Join("/dev/dri", filepath.Base(nodes[0])), nil
}

func renderFiles(dev device) ([]func() (*kdrm.Device, error), error) {
	var st unix.Stat_t
	if err := unix.Fstat(dev.FD(), &st); err != nil {
		return nil, fmt.Errorf("drm: stat %s: %w", dev.Path(), err)
	}
	node, err := renderNodeOf(sysDevChar, unix.Major(st.Rdev), unix.Minor(st.Rdev))
	if err != nil {
		return nil, fmt.Errorf("drm: %w", err)
	}
	second := func() (*kdrm.Device, error) { return secondFileOn(dev) }
	if node == "" {
		return []func() (*kdrm.Device, error){second}, nil
	}
	return []func() (*kdrm.Device, error){func() (*kdrm.Device, error) { return kdrm.Open(node) }, second}, nil
}

func secondFileOn(dev device) (*kdrm.Device, error) {
	reopened, err := kdrm.Open(dev.Path())
	if err != nil {
		return nil, err
	}
	if err := reopened.DropMaster(); err != nil && !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.EACCES) {
		return nil, errors.Join(fmt.Errorf("drm: leave a second file on %s without master: %w", dev.Path(), err), reopened.Close())
	}
	return reopened, nil
}
