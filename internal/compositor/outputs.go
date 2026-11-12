package compositor

import (
	"errors"
	"fmt"
	"image"
	"maps"
	"slices"
	"strings"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/colorspace"
	"github.com/stubbedev/lys-wm/internal/output"
	"github.com/stubbedev/lys-wm/internal/outputmanagement"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/wm"
)

type OutputRule struct {
	Match         wm.OutputMatch
	Mode          ModePolicy
	Position      *image.Point
	Scale         output.Scale
	Depth         backend.BitDepth
	VRR           VRRPolicy
	Disabled      bool
	Shader        string
	HDR           HDRPolicy
	SDRBrightness float64
	HDRPeak       float64
	ICC           *colorspace.Description
	ICCPath       string
	HotCorners    *HotCornerSet
	Transform     render.Transform
	Mirror        *wm.OutputMatch
	Reserved      Insets
}

type Insets struct {
	Top, Right, Bottom, Left int
}

const refreshToleranceMHz = 1000

type OutputRules struct {
	Named    []OutputRule
	Fallback OutputRule
}

func (rs OutputRules) For(b backend.Output) OutputRule {
	if i := slices.IndexFunc(rs.Named, func(r OutputRule) bool { return r.matches(b) }); i >= 0 {
		return rs.Named[i]
	}
	return rs.Fallback
}

func (r OutputRule) matches(b backend.Output) bool {
	return r.Match.Matches(b.Name(), b.Description())
}

func (r OutputRule) scale() output.Scale {
	if r.Scale == 0 {
		return output.Unit
	}
	return r.Scale
}

func internalPanel(name string) bool {
	return strings.HasPrefix(name, "eDP")
}

func (c *Compositor) configuredRule(b backend.Output) OutputRule {
	if rule, ok := c.outputOverrides[b.Name()]; ok {
		return rule
	}
	rule := c.outputRules.For(b)
	if s := c.virtualScale(b); s != 0 {
		rule.Scale = s
	}
	return rule
}

func (c *Compositor) outputRule(b backend.Output) OutputRule {
	rule := c.configuredRule(b)
	if internalPanel(b.Name()) && c.clamshell() {
		rule.Disabled = true
	}
	return rule
}

type ModePolicy interface {
	choose(modes []backend.Mode, current backend.Mode) (backend.Mode, error)
}

type (
	PreferredMode struct{}
	HighResMode   struct{}
	ExactMode     struct {
		Size       image.Point
		RefreshMHz int32
	}
)

func (PreferredMode) choose(modes []backend.Mode, current backend.Mode) (backend.Mode, error) {
	if i := slices.IndexFunc(modes, func(m backend.Mode) bool { return m.Preferred }); i >= 0 {
		return modes[i], nil
	}
	return current, nil
}

func (HighResMode) choose(modes []backend.Mode, current backend.Mode) (backend.Mode, error) {
	best := current
	for _, m := range modes {
		area, bestArea := m.Size.X*m.Size.Y, best.Size.X*best.Size.Y
		if area > bestArea || area == bestArea && m.RefreshMHz > best.RefreshMHz {
			best = m
		}
	}
	return best, nil
}

func (e ExactMode) choose(modes []backend.Mode, _ backend.Mode) (backend.Mode, error) {
	var best backend.Mode
	found := false
	for _, m := range modes {
		if m.Size != e.Size {
			continue
		}
		if e.RefreshMHz == 0 {
			if !found || m.RefreshMHz > best.RefreshMHz {
				best, found = m, true
			}
			continue
		}
		off := abs(m.RefreshMHz - e.RefreshMHz)
		if off <= refreshToleranceMHz && (!found || off < abs(best.RefreshMHz-e.RefreshMHz)) {
			best, found = m, true
		}
	}
	if !found {
		return backend.Mode{}, fmt.Errorf("no mode %dx%d@%dmHz among %v", e.Size.X, e.Size.Y, e.RefreshMHz, modes)
	}
	return best, nil
}

func (r OutputRule) mode() ModePolicy {
	if r.Mode == nil {
		return PreferredMode{}
	}
	return r.Mode
}

func abs(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}

func (c *Compositor) commitOutput(b backend.Output, rule OutputRule) (changed, recolored bool, err error) {
	current := b.State()
	next := current
	switch {
	case rule.Disabled:
		next.Power = backend.Disabled
	case !current.Enabled():
		next.Power = backend.PoweredOn
	}
	if next.Enabled() {
		m, err := rule.mode().choose(b.Modes(), current.Mode)
		if err != nil {
			return false, false, fmt.Errorf("compositor: output %s: %w", b.Name(), err)
		}
		next.Mode, next.Depth, next.HDR = m, rule.Depth, backend.HDRMetadata{}
	}
	var color *colorspace.Output
	if next.Enabled() && c.signalsHDR(b, rule) {
		co, meta, err := c.hdrAsked(b, rule)
		if err != nil {
			c.report(fmt.Errorf("compositor: output %s stays SDR: %w", b.Name(), err))
		} else {
			color, next.Depth, next.HDR = &co, backend.Depth10, meta
			if rule.Shader != "" {
				c.report(fmt.Errorf("compositor: output %s: screen shader %s stays off while it is in HDR: shaders work on SDR code values", b.Name(), rule.Shader))
			}
		}
	}
	changed = next != current
	if changed {
		err = b.Commit(next)
		if color != nil && (errors.Is(err, backend.ErrHDRUnsupported) || errors.Is(err, backend.ErrDepthUnsupported)) {
			c.report(fmt.Errorf("compositor: output %s stays SDR: %w", b.Name(), err))
			color, next.Depth, next.HDR = nil, rule.Depth, backend.HDRMetadata{}
			changed = next != current
			err = nil
			if changed {
				err = b.Commit(next)
			}
		}
		if errors.Is(err, backend.ErrDepthUnsupported) {
			c.report(fmt.Errorf("compositor: output %s shows 8-bit frames instead: %w", b.Name(), err))
			next.Depth = backend.Depth8
			changed = next != current
			err = nil
			if changed {
				err = b.Commit(next)
			}
		}
		if err != nil {
			return false, false, fmt.Errorf("compositor: output %s: %w", b.Name(), err)
		}
	}
	if !next.Enabled() {
		delete(c.hdr, b)
		delete(c.profiles, b)
		return changed, false, nil
	}
	recolored, err = c.setOutputColor(b, color)
	if color != nil {
		delete(c.profiles, b)
	} else {
		err = errors.Join(err, c.applyProfile(b, rule))
	}
	return changed, recolored, errors.Join(c.commitAdaptiveSync(b, rule.VRR), err)
}

func (c *Compositor) relayout() error {
	c.hot.forget()
	right := 0
	for _, o := range c.outputs {
		if at := c.outputRule(o.Backend()).Position; at != nil {
			right = max(right, at.X+o.LogicalSize().X)
		}
	}
	var errs []error
	for _, o := range c.outputs {
		at := image.Pt(right, 0)
		if fixed := c.outputRule(o.Backend()).Position; fixed != nil {
			at = *fixed
		} else {
			right += o.LogicalSize().X
		}
		if o.Position() != at {
			errs = append(errs, o.Update(at, o.Scale(), o.Transform()))
		}
	}
	return errors.Join(append(errs, c.shell.ReconfigureLock(), c.confirmLock(), c.outputConfig.Changed())...)
}

func (c *Compositor) output(b backend.Output) *output.Output {
	for _, o := range c.outputs {
		if o.Backend() == b {
			return o
		}
	}
	return nil
}

func (c *Compositor) knownOutput(b backend.Output, event string) (*output.Output, error) {
	if o := c.output(b); o != nil {
		return o, nil
	}
	return nil, fmt.Errorf("compositor: %s unknown output %s", event, b.Name())
}

func (c *Compositor) outputLive(o *output.Output) bool {
	return o != nil && c.output(o.Backend()) == o
}

func (c *Compositor) OutputAdded(b backend.Output) error {
	c.stateChanged()
	return errors.Join(c.addOutput(b), c.dockChanged(b))
}

func (c *Compositor) addOutput(b backend.Output) error {
	c.capFrameRate(b)
	rule := c.outputRule(b)
	if _, _, err := c.commitOutput(b, rule); err != nil {
		c.report(err)
	}
	if !b.State().Enabled() {
		c.disabled = append(c.disabled, b)
		return c.outputConfig.Changed()
	}
	if rule.Mirror != nil {
		return c.addMirror(b, rule)
	}
	return c.enable(b, rule)
}

func (c *Compositor) enable(b backend.Output, rule OutputRule) error {
	o, err := c.layout.Add(b, image.Point{}, rule.scale(), rule.Transform)
	if err != nil {
		return err
	}
	c.outputs = append(c.outputs, o)
	if err := c.keepInherited(o); err != nil {
		return err
	}
	c.addCursorPlane(o)
	c.preloadCursorsLater(o.Scale().ToDevice(c.cursors.Size))
	if err := c.relayout(); err != nil {
		return err
	}
	if len(c.outputs) == 1 {
		center := o.Logical().Min.Add(o.LogicalSize().Div(2))
		c.cursor = point{float64(center.X), float64(center.Y)}
	}
	if _, err := c.arrange(o); err != nil {
		return err
	}
	c.syncWorkspaceRules()
	if err := c.update(c.wm.AddOutput(b.Name(), o.Logical(), c.usable[o].Add(o.Position()))); err != nil {
		return err
	}
	return errors.Join(c.syncAreas(), c.scheduleAll())
}

func (c *Compositor) OutputRemoved(b backend.Output) error {
	c.stateChanged()
	if m := c.mirrorOf(b); m != nil {
		return c.dropMirror(m)
	}
	err := errors.Join(c.removeOutput(b), c.dockChanged(b))
	c.applyLidPolicy()
	return err
}

func (c *Compositor) removeOutput(b backend.Output) error {
	if i := slices.Index(c.disabled, b); i >= 0 {
		c.disabled = slices.Delete(c.disabled, i, i+1)
		return c.outputConfig.Changed()
	}
	o, err := c.knownOutput(b, "removal of")
	if err != nil {
		return err
	}
	return c.disable(o)
}

func (c *Compositor) disable(o *output.Output) error {
	if c.hold.target.o == o {
		c.dropDragHold()
	}
	b := o.Backend()
	c.outputs = slices.DeleteFunc(c.outputs, func(x *output.Output) bool { return x == o })
	delete(c.shown, o)
	errs := []error{c.castGone(nil, o)}
	if hw, ok := c.hwCursors[o]; ok {
		errs = append(errs, hw.release())
		delete(c.hwCursors, o)
	}
	delete(c.overlays, o)
	c.dropCandidates(o)
	errs = append(errs, c.dropHDRSwitch(o.Backend()))
	delete(c.profiles, o.Backend())
	delete(c.blanked, o)
	errs = append(errs, c.fill(o, nil, nil), c.dropCaptures(o), c.closeLayers(o), o.Remove())
	delete(c.usable, o)
	if sc, ok := c.scenes[o]; ok {
		errs = append(errs, sc.blurs.drop(), sc.dropImage(), sc.dropWorking())
	}
	delete(c.scenes, o)
	delete(c.hdr, b)
	if c.colorManager != nil {
		c.colorManager.OutputRemoved(o)
	}
	delete(c.dropped, o)
	delete(c.switches, o)
	delete(c.scratchSlideOf, o)
	delete(c.peeks, o)
	errs = append(errs, c.endInherited(o), c.dropLabels(o), c.overviewOutputGone(o), c.switcherOutputGone(o), c.dropClosingOn(o), c.dropLockSnapshotOn(o), c.power.Removed(o), c.gamma.Removed(o))
	c.ctm.Removed(o)
	for _, e := range c.entered {
		if slices.Contains(e.outputs, o) {
			errs = append(errs, e.s.Leave(o.Resources(e.s.Client())))
			e.outputs = slices.DeleteFunc(e.outputs, func(x *output.Output) bool { return x == o })
		}
	}
	for _, w := range c.windows {
		if w.output == o {
			w.output = nil
		}
	}
	s, err := c.wm.RemoveOutput(b.Name())
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	c.wm = s
	c.syncWorkspaceRules()
	c.stateChanged()
	errs = append(errs, c.relayout(), c.syncAreas(), c.fifo.Latched(nil), c.scheduleMirrorsOf(o))
	return errors.Join(errs...)
}

func (c *Compositor) Heads() []outputmanagement.Head {
	heads := make([]outputmanagement.Head, 0, len(c.outputs)+len(c.disabled))
	for _, o := range c.outputs {
		b := o.Backend()
		as, ok := b.(backend.AdaptiveSync)
		heads = append(heads, outputmanagement.Head{Output: b, Enabled: true, Mode: b.State().Mode, Position: o.Position(), Scale: o.Scale(), Transform: o.Transform(), AdaptiveSync: ok && as.AdaptiveSync()})
	}
	for _, b := range c.disabled {
		heads = append(heads, outputmanagement.Head{Output: b})
	}
	slices.SortFunc(heads, func(a, b outputmanagement.Head) int { return strings.Compare(a.Output.Name(), b.Output.Name()) })
	return heads
}

func (c *Compositor) Configure(heads []outputmanagement.HeadConfig, apply bool) error {
	overrides := maps.Clone(c.outputOverrides)
	if overrides == nil {
		overrides = map[string]OutputRule{}
	}
	lit := false
	for _, h := range heads {
		b := h.Output
		rule := c.configuredRule(b)
		rule.Disabled = !h.Enabled
		if h.Mode != nil {
			rule.Mode = ExactMode{Size: h.Mode.Size, RefreshMHz: h.Mode.RefreshMHz}
		}
		if h.Position != nil {
			at := *h.Position
			rule.Position = &at
		}
		if h.Scale != nil {
			rule.Scale = *h.Scale
		}
		if h.Transform != nil {
			rule.Transform = *h.Transform
		}
		if h.AdaptiveSync != nil {
			rule.VRR = VRROff
			if *h.AdaptiveSync {
				rule.VRR = VRROn
			}
		}
		overrides[b.Name()] = rule
		if !h.Enabled {
			continue
		}
		lit = true
		if _, err := rule.mode().choose(b.Modes(), b.State().Mode); err != nil {
			return fmt.Errorf("%w: output %s: %w", outputmanagement.ErrRefused, b.Name(), err)
		}
	}
	if !lit {
		return fmt.Errorf("%w: a configuration that turns off every output would leave nothing on screen", outputmanagement.ErrRefused)
	}
	if !apply {
		return nil
	}
	previous := c.outputOverrides
	c.outputOverrides = overrides
	if err := c.applyOutputRules(); err != nil {
		c.outputOverrides = previous
		return errors.Join(err, c.applyOutputRules())
	}
	return nil
}

func (c *Compositor) OutputDamaged(b backend.Output) error {
	o, err := c.knownOutput(b, "damage on")
	if err != nil {
		return err
	}
	c.sceneOf(o).invalidate()
	return c.schedule(o)
}

func (c *Compositor) OutputChanged(b backend.Output) error {
	c.stateChanged()
	if slices.Contains(c.disabled, b) {
		return nil
	}
	o, err := c.knownOutput(b, "change of")
	if err != nil {
		return err
	}
	return c.outputResized(o)
}

func (c *Compositor) outputResized(o *output.Output) error {
	if err := errors.Join(o.Changed(), c.relayout()); err != nil {
		return err
	}
	_, err := c.arrange(o)
	return errors.Join(err, c.syncAreas(), c.schedule(o))
}

func (c *Compositor) SetOutputScale(b backend.Output, scale output.Scale) error {
	o, err := c.knownOutput(b, "scale for")
	if err != nil {
		return err
	}
	if err := o.Update(o.Position(), scale, o.Transform()); err != nil {
		return err
	}
	return c.outputResized(o)
}

func (c *Compositor) applyOutputRules() error {
	var errs []error
	for _, o := range slices.Clone(c.outputs) {
		b := o.Backend()
		rule := c.outputRule(b)
		if rule.Disabled {
			if _, _, err := c.commitOutput(b, rule); err != nil {
				errs = append(errs, err)
				continue
			}
			c.disabled = append(c.disabled, b)
			errs = append(errs, c.disable(o))
			continue
		}
		if rule.Mirror != nil {
			errs = append(errs, c.disable(o))
			if _, _, err := c.commitOutput(b, rule); err != nil {
				errs = append(errs, err)
			}
			errs = append(errs, c.addMirror(b, rule))
			continue
		}
		changed, recolored, err := c.commitOutput(b, rule)
		if err != nil {
			errs = append(errs, err)
		}
		switch {
		case changed || o.Scale() != rule.scale() || o.Transform() != rule.Transform:
			errs = append(errs, o.Update(o.Position(), rule.scale(), rule.Transform), c.outputResized(o))
		case recolored && c.colorManager != nil:
			errs = append(errs, c.colorManager.OutputDone(o))
		}
	}
	for _, b := range slices.Clone(c.disabled) {
		rule := c.outputRule(b)
		if rule.Disabled {
			continue
		}
		if _, _, err := c.commitOutput(b, rule); err != nil {
			errs = append(errs, err)
			continue
		}
		c.disabled = slices.DeleteFunc(c.disabled, func(x backend.Output) bool { return x == b })
		if rule.Mirror != nil {
			errs = append(errs, c.addMirror(b, rule))
			continue
		}
		errs = append(errs, c.enable(b, rule))
	}
	errs = append(errs, c.applyMirrorRules())
	errs = append(errs, c.relayout(), c.syncAreas())
	return errors.Join(errs...)
}

func (i Insets) inset(r image.Rectangle) image.Rectangle {
	out := image.Rect(r.Min.X+i.Left, r.Min.Y+i.Top, r.Max.X-i.Right, r.Max.Y-i.Bottom)
	if out.Empty() {
		return image.Rectangle{Min: r.Min, Max: r.Min}
	}
	return out
}
