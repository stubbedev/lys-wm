package drm

import (
	"errors"
	"fmt"
	"image"

	"github.com/stubbedev/lys-lib/fourcc"

	"github.com/stubbedev/lys-wm/internal/render"
)

type lazyBlit struct {
	open      func() (*blitAllocator, error)
	primary   render.Renderer
	modifiers map[fourcc.Format][]fourcc.Modifier
	blit      *blitAllocator
	held      int
}

func newLazyBlit(probed *blitAllocator, open func() (*blitAllocator, error)) (*lazyBlit, error) {
	l := &lazyBlit{open: open, primary: probed.renderer(), modifiers: map[fourcc.Format][]fourcc.Modifier{}}
	for _, f := range append([]fourcc.Format{scanoutFormat}, deepFormats...) {
		mods, err := probed.renderable(f)
		if err != nil {
			return nil, errors.Join(err, probed.close())
		}
		l.modifiers[f] = mods
	}
	return l, probed.close()
}

func (l *lazyBlit) renderer() render.Renderer {
	return l.primary
}

func (l *lazyBlit) renderable(format fourcc.Format) ([]fourcc.Modifier, error) {
	mods, ok := l.modifiers[format]
	if !ok {
		return nil, fmt.Errorf("drm: the secondary card was not asked for %v when it was probed", format)
	}
	return mods, nil
}

func (l *lazyBlit) allocate(size image.Point, format fourcc.Format, modifiers []fourcc.Modifier) (*scanout, error) {
	if l.blit == nil {
		b, err := l.open()
		if err != nil {
			return nil, fmt.Errorf("drm: open the secondary card's render stack: %w", err)
		}
		l.blit = b
	}
	s, err := l.blit.allocate(size, format, modifiers)
	if err != nil {
		return nil, errors.Join(err, l.idle())
	}
	l.held++
	release := s.release
	s.release = func() error {
		err := release()
		l.held--
		return errors.Join(err, l.idle())
	}
	return s, nil
}

func (l *lazyBlit) idle() error {
	if l.held > 0 || l.blit == nil {
		return nil
	}
	b := l.blit
	l.blit = nil
	return b.close()
}

func (l *lazyBlit) fence() (int, error) {
	if l.blit == nil {
		return -1, nil
	}
	return l.blit.fence()
}

func (l *lazyBlit) close() error {
	b := l.blit
	l.blit, l.held = nil, 0
	if b == nil {
		return nil
	}
	return b.close()
}
