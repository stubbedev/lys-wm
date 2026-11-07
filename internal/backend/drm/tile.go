package drm

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"slices"
	"strconv"

	kdrm "github.com/stubbedev/lys-lib/drm"
)

const propTile = "TILE"

type tileInfo struct {
	group      uint32
	cols, rows int
	col, row   int
	size       image.Point
}

func (t tileInfo) full() image.Point {
	return image.Pt(t.cols*t.size.X, t.rows*t.size.Y)
}

func (t tileInfo) at() image.Point {
	return image.Pt(t.col*t.size.X, t.row*t.size.Y)
}

func parseTile(blob []byte) (tileInfo, error) {
	fields := bytes.Split(bytes.TrimRight(blob, "\x00"), []byte(":"))
	if len(fields) != 8 {
		return tileInfo{}, fmt.Errorf("drm: a TILE blob of %d fields, want 8: %q", len(fields), blob)
	}
	var v [8]int
	for i, f := range fields {
		n, err := strconv.Atoi(string(f))
		if err != nil || n < 0 {
			return tileInfo{}, fmt.Errorf("drm: field %d of the TILE blob %q is not a count", i, blob)
		}
		v[i] = n
	}
	t := tileInfo{group: uint32(v[0]), cols: v[2], rows: v[3], col: v[4], row: v[5], size: image.Pt(v[6], v[7])} //nolint:gosec // tile group IDs are 32-bit
	if t.group == 0 || t.cols <= 0 || t.rows <= 0 || t.col >= t.cols || t.row >= t.rows || t.size.X <= 0 || t.size.Y <= 0 {
		return tileInfo{}, fmt.Errorf("drm: the TILE blob %q places no tile", blob)
	}
	return t, nil
}

type tileMember struct {
	c    kdrm.Connector
	info tileInfo
}

func (b *Backend) tileOf(c kdrm.Connector) (tileInfo, bool, error) {
	props, err := b.dev.Properties(c.ID, kdrm.ObjectConnector)
	if err != nil {
		return tileInfo{}, false, err
	}
	p, ok := props[propTile]
	if !ok || p.Value == 0 {
		return tileInfo{}, false, nil
	}
	data, err := b.dev.Blob(uint32(p.Value)) //nolint:gosec // property blob IDs are 32-bit
	if err != nil {
		return tileInfo{}, false, fmt.Errorf("drm: the TILE of %s: %w", c.Name(), err)
	}
	t, err := parseTile(data)
	if err != nil {
		return tileInfo{}, false, fmt.Errorf("drm: %s: %w", c.Name(), err)
	}
	return t, true, nil
}

type tileGroup struct {
	members []tileMember
	whole   *tileMember
}

func (g *tileGroup) complete() bool {
	t := g.members[0].info
	if len(g.members) != t.cols*t.rows {
		return false
	}
	for _, m := range g.members {
		if m.info.cols != t.cols || m.info.rows != t.rows || m.info.size != t.size {
			return false
		}
	}
	return true
}

func (g *tileGroup) owns(id uint32) bool {
	return slices.ContainsFunc(g.members, func(m tileMember) bool { return m.c.ID == id })
}

func fullModeOf(c kdrm.Connector, full image.Point) bool {
	return slices.ContainsFunc(c.Modes, func(m kdrm.Mode) bool { return int(m.HDisplay) >= full.X && int(m.VDisplay) >= full.Y })
}

func (b *Backend) tileGroups(connected []kdrm.Connector) (map[uint32]*tileGroup, error) {
	groups := map[uint32]*tileGroup{}
	var errs []error
	for _, c := range connected {
		t, ok, err := b.tileOf(c)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !ok {
			continue
		}
		g := groups[t.group]
		if g == nil {
			g = &tileGroup{}
			groups[t.group] = g
		}
		g.members = append(g.members, tileMember{c: c, info: t})
	}
	for id, g := range groups {
		if !g.complete() {
			delete(groups, id)
			continue
		}
		slices.SortFunc(g.members, func(a, b tileMember) int {
			if a.info.row != b.info.row {
				return a.info.row - b.info.row
			}
			return a.info.col - b.info.col
		})
		full := g.members[0].info.full()
		if i := slices.IndexFunc(g.members, func(m tileMember) bool { return fullModeOf(m.c, full) }); i >= 0 {
			g.whole = &g.members[i]
		}
	}
	return groups, errors.Join(errs...)
}

func tileMode(c kdrm.Connector, size image.Point) (kdrm.Mode, bool) {
	var best kdrm.Mode
	found := false
	for _, m := range c.Modes {
		if int(m.HDisplay) != size.X || int(m.VDisplay) != size.Y {
			continue
		}
		if !found || m.Preferred() && !best.Preferred() || m.Preferred() == best.Preferred() && m.RefreshMHz() > best.RefreshMHz() {
			best, found = m, true
		}
	}
	return best, found
}

func combinedMode(m kdrm.Mode, cols, rows int) kdrm.Mode {
	c := m
	c.HDisplay, c.HSyncStart, c.HSyncEnd, c.HTotal = m.HDisplay*uint16(cols), m.HSyncStart*uint16(cols), m.HSyncEnd*uint16(cols), m.HTotal*uint16(cols) //nolint:gosec // a tiled display's columns are few
	c.VDisplay, c.VSyncStart, c.VSyncEnd, c.VTotal = m.VDisplay*uint16(rows), m.VSyncStart*uint16(rows), m.VSyncEnd*uint16(rows), m.VTotal*uint16(rows) //nolint:gosec // a tiled display's rows are few
	c.Clock = m.Clock * uint32(cols*rows)                                                                                                               //nolint:gosec // a tiled display's tiles are few
	c.Type |= kdrm.ModeTypePreferred
	return c
}

type tile struct {
	conn      kdrm.Connector
	connector kmsObject
	crtc      *crtc
	primary   *plane
	mode      kdrm.Mode
	modeBlob  uint32
	at        image.Point
}

func (t *tile) bound() bool {
	return t.crtc != nil && t.crtc.output != nil && t.primary != nil
}

func (b *Backend) newTiles(g *tileGroup) (kdrm.Connector, kdrm.Mode, []*tile, error) {
	leader := g.members[0]
	size := leader.info.size
	lead, ok := tileMode(leader.c, size)
	if !ok {
		return kdrm.Connector{}, kdrm.Mode{}, nil, fmt.Errorf("drm: %s lists no %v mode for its tile", leader.c.Name(), size)
	}
	tiles := make([]*tile, 0, len(g.members)-1)
	release := func() error {
		errs := make([]error, 0, len(tiles))
		for _, t := range tiles {
			errs = append(errs, b.dev.DestroyBlob(t.modeBlob))
		}
		return errors.Join(errs...)
	}
	for _, m := range g.members[1:] {
		mode, ok := tileMode(m.c, size)
		if !ok {
			return kdrm.Connector{}, kdrm.Mode{}, nil, errors.Join(fmt.Errorf("drm: %s lists no %v mode for its tile", m.c.Name(), size), release())
		}
		props, err := b.dev.Properties(m.c.ID, kdrm.ObjectConnector)
		if err != nil {
			return kdrm.Connector{}, kdrm.Mode{}, nil, errors.Join(err, release())
		}
		blob, err := b.dev.CreateBlob(mode.Bytes())
		if err != nil {
			return kdrm.Connector{}, kdrm.Mode{}, nil, errors.Join(err, release())
		}
		tiles = append(tiles, &tile{conn: m.c, connector: kmsObject{m.c.ID, props}, mode: mode, modeBlob: blob, at: m.info.at()})
	}
	c := leader.c
	c.Modes = []kdrm.Mode{combinedMode(lead, leader.info.cols, leader.info.rows)}
	return c, lead, tiles, nil
}

func (o *Output) tiled() bool {
	return len(o.tiles) > 0
}

func (o *Output) ownsConnector(id uint32) bool {
	return o.conn.ID == id || slices.ContainsFunc(o.tiles, func(t *tile) bool { return t.conn.ID == id })
}

func (o *Output) crtcMode(m kdrm.Mode) kdrm.Mode {
	if o.tiled() {
		return o.tileMode
	}
	return m
}

func (o *Output) bindTiles() error {
	for _, t := range o.tiles {
		if t.bound() {
			continue
		}
		cr, p, err := o.b.pick(t.conn, 0)
		if err != nil {
			o.unbindTiles()
			return fmt.Errorf("drm: the tile of %s on %s: %w", o.Name(), t.conn.Name(), err)
		}
		t.crtc, t.primary = cr, p
		cr.output, p.taken = o, true
	}
	return nil
}

func (o *Output) unbindTiles() {
	for _, t := range o.tiles {
		if !t.bound() {
			continue
		}
		t.crtc.output, t.primary.taken = nil, false
		t.crtc, t.primary = nil, nil
	}
}

func (o *Output) releaseTiles() error {
	o.unbindTiles()
	errs := make([]error, 0, len(o.tiles))
	for _, t := range o.tiles {
		errs = append(errs, o.b.dev.DestroyBlob(t.modeBlob))
	}
	o.tiles = nil
	return errors.Join(errs...)
}

func (o *Output) setCRTCs(r *kdrm.Request, name string, value uint64) {
	o.crtc.set(r, name, value)
	for _, t := range o.tiles {
		t.crtc.set(r, name, value)
	}
}

func (o *Output) setConnectors(r *kdrm.Request, name string, value uint64) {
	o.connector.set(r, name, value)
	for _, t := range o.tiles {
		t.connector.set(r, name, value)
	}
}

func (o *Output) addModeset(r *kdrm.Request, blob uint32) {
	o.connector.set(r, "CRTC_ID", uint64(o.crtc.id))
	o.crtc.set(r, "MODE_ID", uint64(blob))
	o.crtc.set(r, "ACTIVE", 1)
	for _, t := range o.tiles {
		t.connector.set(r, "CRTC_ID", uint64(t.crtc.id))
		t.crtc.set(r, "MODE_ID", uint64(t.modeBlob))
		t.crtc.set(r, "ACTIVE", 1)
	}
}

func (o *Output) placeTiles(fb uint32, fence int) {
	for _, t := range o.tiles {
		o.placeOn(t.primary, t.crtc, fb, t.at, image.Rectangle{Max: image.Pt(int(t.mode.HDisplay), int(t.mode.VDisplay))}, fence)
	}
}

func (o *Output) flipTiles(fb uint32, fence int) {
	r := &o.b.req
	for _, t := range o.tiles {
		t.primary.set(r, "FB_ID", uint64(fb))
		if _, ok := t.primary.props["FB_DAMAGE_CLIPS"]; ok {
			t.primary.set(r, "FB_DAMAGE_CLIPS", uint64(o.damageBlob))
		}
		if fence >= 0 {
			t.primary.set(r, "IN_FENCE_FD", uint64(fence))
		}
	}
}

func (o *Output) detachTiles(r *kdrm.Request, crtcs bool) {
	for _, t := range o.tiles {
		if !t.bound() {
			continue
		}
		t.primary.set(r, "FB_ID", 0)
		t.primary.set(r, "CRTC_ID", 0)
		if crtcs {
			t.connector.set(r, "CRTC_ID", 0)
			t.crtc.set(r, "MODE_ID", 0)
		}
		t.crtc.set(r, "ACTIVE", 0)
	}
}

type tiling struct {
	mode  kdrm.Mode
	tiles []*tile
}

func (t *tiling) release(dev device) error {
	errs := make([]error, 0, len(t.tiles))
	for _, tl := range t.tiles {
		errs = append(errs, dev.DestroyBlob(tl.modeBlob))
	}
	t.tiles = nil
	return errors.Join(errs...)
}
