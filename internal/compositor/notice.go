package compositor

import (
	"errors"
	"fmt"
	"image"

	"github.com/stubbedev/lys-wm/internal/output"
	"github.com/stubbedev/lys-wm/internal/render"
)

const (
	noticeLineHeight = 24
	noticeTextPx     = 14
	noticeMaxLines   = 6
)

var noticeColors = TabColors{
	Background: render.Color{R: 0xb0, G: 0x20, B: 0x20, A: 0xff},
	Text:       render.Color{R: 0xff, G: 0xff, B: 0xff, A: 0xff},
}

type ConfigProblems struct {
	Rejected bool
	Messages []string
}

func (c *Compositor) ShowConfigProblems(p ConfigProblems) {
	c.configProblems, c.noticeDismissed = p, false
	c.stateChanged()
	if err := c.scheduleAll(); err != nil {
		c.report(fmt.Errorf("compositor: show the config problems: %w", err))
	}
}

func (c *Compositor) dismissConfigNotice() {
	c.noticeDismissed = true
	if err := c.scheduleAll(); err != nil {
		c.report(fmt.Errorf("compositor: hide the config problems: %w", err))
	}
}

func (p ConfigProblems) noticeLines() []string {
	if len(p.Messages) == 0 {
		return nil
	}
	head := "lys-wm config: left out of what is running"
	if p.Rejected {
		head = "lys-wm config: rejected, the previous config is still running"
	}
	lines := append([]string{head}, p.Messages...)
	if len(lines) > noticeMaxLines {
		more := len(lines) - noticeMaxLines + 1
		lines = append(lines[:noticeMaxLines-1:noticeMaxLines-1], fmt.Sprintf("and %d more: lys-wmctl configproblems", more))
	}
	return lines
}

func (c *Compositor) drawConfigNotice(pass *frame, o *output.Output) error {
	if c.noticeDismissed {
		return nil
	}
	s := o.Scale()
	width := o.Backend().State().Mode.Size.X
	height, px := s.ToDevice(noticeLineHeight), s.ToDevice(noticeTextPx)
	for i, line := range c.configProblems.noticeLines() {
		tex, err := c.label(o, labelKey{text: line, size: image.Pt(width, height), px: px, colors: noticeColors})
		if err != nil {
			return errors.Join(errors.New("compositor: the config notice"), err)
		}
		size := tex.Size()
		pass.ops = append(pass.ops, drawOp{tex: tex, src: render.FRect{W: float64(size.X), H: float64(size.Y)}, dst: image.Rect(0, i*height, width, (i+1)*height), alpha: 1})
	}
	return nil
}
