// Package xcursor reads Xcursor files and resolves cursor themes the way
// libXcursor does: XCURSOR_PATH, theme inheritance and nominal sizes.
package xcursor

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const (
	magic      = "Xcur"
	imageType  = 0xfffd0002
	headerSize = 16
	imageSize  = 36
	maxSide    = 0x7fff
)

type Image struct {
	Size    image.Point
	Hotspot image.Point
	Nominal int
	Pixels  []byte
}

func Nearest(r io.ReaderAt, size int64, want int) (Image, error) {
	le := binary.LittleEndian
	var head [headerSize]byte
	if size < headerSize {
		return Image{}, errors.New("xcursor: not an Xcursor file")
	}
	if _, err := r.ReadAt(head[:], 0); err != nil || string(head[:4]) != magic {
		return Image{}, errors.New("xcursor: not an Xcursor file")
	}
	header, ntoc := le.Uint32(head[4:]), le.Uint32(head[12:])
	if int64(header)+int64(ntoc)*12 > size {
		return Image{}, fmt.Errorf("xcursor: table of contents with %d entries does not fit %d bytes", ntoc, size)
	}
	toc := make([]byte, int(ntoc)*12)
	if _, err := r.ReadAt(toc, int64(header)); err != nil {
		return Image{}, fmt.Errorf("xcursor: read the table of contents: %w", err)
	}
	pos, nominal, found := uint32(0), 0, false
	for i := range int(ntoc) {
		entry := toc[i*12:]
		if le.Uint32(entry) != imageType {
			continue
		}
		n := int(le.Uint32(entry[4:]))
		if !found || dist(n, want) < dist(nominal, want) {
			pos, nominal, found = le.Uint32(entry[8:]), n, true
		}
	}
	if !found {
		return Image{}, errors.New("xcursor: no images")
	}
	return readImage(r, size, pos)
}

func readImage(r io.ReaderAt, size int64, pos uint32) (Image, error) {
	le := binary.LittleEndian
	if int64(pos)+imageSize > size {
		return Image{}, fmt.Errorf("xcursor: image header at %d runs past %d bytes", pos, size)
	}
	var h [imageSize]byte
	if _, err := r.ReadAt(h[:], int64(pos)); err != nil {
		return Image{}, fmt.Errorf("xcursor: read the image header at %d: %w", pos, err)
	}
	if le.Uint32(h[:]) != imageSize || le.Uint32(h[4:]) != imageType {
		return Image{}, fmt.Errorf("xcursor: malformed image header at %d", pos)
	}
	nominal, w, ht, xhot, yhot := le.Uint32(h[8:]), le.Uint32(h[16:]), le.Uint32(h[20:]), le.Uint32(h[24:]), le.Uint32(h[28:])
	if w == 0 || ht == 0 || w > maxSide || ht > maxSide || xhot > w || yhot > ht || nominal > maxSide {
		return Image{}, fmt.Errorf("xcursor: image %dx%d with hotspot %d,%d at %d is invalid", w, ht, xhot, yhot, pos)
	}
	start := int64(pos) + imageSize
	pixels := make([]byte, int(w)*int(ht)*4)
	if start+int64(len(pixels)) > size {
		return Image{}, fmt.Errorf("xcursor: %dx%d pixels at %d run past %d bytes", w, ht, pos, size)
	}
	if _, err := r.ReadAt(pixels, start); err != nil {
		return Image{}, fmt.Errorf("xcursor: read %dx%d pixels at %d: %w", w, ht, pos, err)
	}
	return Image{
		Size:    image.Pt(int(w), int(ht)),
		Hotspot: image.Pt(int(xhot), int(yhot)),
		Nominal: int(nominal),
		Pixels:  pixels,
	}, nil
}

func SearchPath(getenv func(string) string) []string {
	if p := getenv("XCURSOR_PATH"); p != "" {
		return filepath.SplitList(p)
	}
	var dirs []string
	if home := getenv("HOME"); home != "" {
		data := getenv("XDG_DATA_HOME")
		if data == "" {
			data = filepath.Join(home, ".local", "share")
		}
		dirs = append(dirs, filepath.Join(data, "icons"), filepath.Join(home, ".icons"))
	}
	xdg := getenv("XDG_DATA_DIRS")
	if xdg == "" {
		xdg = "/usr/local/share:/usr/share"
	}
	for _, d := range filepath.SplitList(xdg) {
		dirs = append(dirs, filepath.Join(d, "icons"))
	}
	return append(dirs, "/usr/share/pixmaps")
}

type Theme struct {
	name string
	dirs []string
}

func Open(name string, searchPath []string) (*Theme, error) {
	t := &Theme{name: name}
	if err := t.resolve(name, searchPath, map[string]bool{}); err != nil {
		return nil, err
	}
	if len(t.dirs) == 0 {
		return nil, fmt.Errorf("xcursor: no cursors for theme %q in %v", name, searchPath)
	}
	return t, nil
}

func (t *Theme) resolve(name string, searchPath []string, visited map[string]bool) error {
	if visited[name] {
		return nil
	}
	visited[name] = true
	var inherits []string
	for _, base := range searchPath {
		dir := filepath.Join(base, name)
		if info, err := os.Stat(filepath.Join(dir, "cursors")); err == nil && info.IsDir() && !slices.Contains(t.dirs, filepath.Join(dir, "cursors")) {
			t.dirs = append(t.dirs, filepath.Join(dir, "cursors"))
		}
		parents, err := readInherits(filepath.Join(dir, "index.theme"))
		if err != nil {
			return err
		}
		inherits = append(inherits, parents...)
	}
	for _, parent := range inherits {
		if err := t.resolve(parent, searchPath, visited); err != nil {
			return err
		}
	}
	return nil
}

func readInherits(path string) ([]string, error) {
	f, err := os.Open(path) //nolint:gosec // theme files come from the user's XCURSOR_PATH
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("xcursor: open %s: %w", path, err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		key, value, ok := strings.Cut(sc.Text(), "=")
		if ok && strings.TrimSpace(key) == "Inherits" {
			var out []string
			for p := range strings.FieldsFuncSeq(value, func(r rune) bool { return r == ',' || r == ';' || r == ' ' }) {
				out = append(out, p)
			}
			return out, nil
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("xcursor: read %s: %w", path, err)
	}
	return nil, nil
}

func (t *Theme) Load(size int, names ...string) (Image, error) {
	for _, name := range names {
		img, ok, err := t.nearest(name, size)
		if err != nil {
			return Image{}, err
		}
		if ok {
			return img, nil
		}
	}
	return Image{}, fmt.Errorf("xcursor: theme %q has none of %v", t.name, names)
}

func (t *Theme) nearest(name string, size int) (Image, bool, error) {
	for _, dir := range t.dirs {
		f, err := os.Open(filepath.Join(dir, name)) //nolint:gosec // theme files come from the user's XCURSOR_PATH
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return Image{}, false, fmt.Errorf("xcursor: open %s from theme %q: %w", name, t.name, err)
		}
		img, err := nearestIn(f, size)
		if err != nil {
			return Image{}, false, fmt.Errorf("xcursor: %s in theme %q: %w", name, t.name, err)
		}
		return img, true, nil
	}
	return Image{}, false, nil
}

func nearestIn(f *os.File, size int) (img Image, err error) {
	defer func() { err = errors.Join(err, f.Close()) }()
	info, err := f.Stat()
	if err != nil {
		return Image{}, err
	}
	return Nearest(f, info.Size(), size)
}

func dist(a, b int) int {
	if a > b {
		return a - b
	}
	return b - a
}
