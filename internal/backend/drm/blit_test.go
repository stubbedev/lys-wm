package drm

import (
	"errors"
	"fmt"
	"image"
	"slices"
	"strings"
	"testing"

	kdrm "github.com/stubbedev/lys-lib/drm"
	"github.com/stubbedev/lys-lib/fourcc"
	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/gpu"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/render/soft"
)

type blitWorld struct {
	memory map[int]*soft.Buffer
	log    []string
}

type fakeGBM struct {
	world *blitWorld
}

func (g fakeGBM) Allocate(size image.Point, format fourcc.Format, modifiers []fourcc.Modifier, _ gpu.Usage) (*gpu.Buffer, error) {
	fd, err := unix.MemfdCreate("blit", unix.MFD_CLOEXEC)
	if err != nil {
		return nil, err
	}
	modifier := fourcc.Linear
	if len(modifiers) > 0 {
		modifier = modifiers[0]
	}
	g.world.memory[fd] = soft.NewBuffer(size.X, size.Y)
	d := gpu.DMABUF{Size: size, Format: format, Modifier: modifier, Planes: []gpu.Plane{{FD: fd, Stride: uint32(size.X * 4)}}}
	return gpu.NewBuffer(d, func() {}), nil
}

type sharedTexture struct {
	tex render.Texture
	buf *soft.Buffer
}

type fakeRenderer struct {
	*soft.Renderer
	imported  []sharedTexture
	world     *blitWorld
	name      string
	native    bool
	unlinear  bool
	mainError error
}

func (r *fakeRenderer) RenderableModifiers(fourcc.Format) ([]fourcc.Modifier, error) {
	return []fourcc.Modifier{fourcc.Linear}, nil
}

func (r *fakeRenderer) ImportTarget(b gpu.DMABUF) (render.ReadTarget, error) {
	if r.unlinear && b.Modifier == fourcc.Linear {
		return nil, fmt.Errorf("%s renders into no linear buffer", r.name)
	}
	return r.world.memory[b.Planes[0].FD], nil
}

func (r *fakeRenderer) ImportTexture(b gpu.DMABUF) (render.Texture, error) {
	buf := r.world.memory[b.Planes[0].FD]
	tex, err := r.NewTexture(render.ARGB8888, b.Size, buf.Stride, buf.Pix)
	if err == nil {
		r.imported = append(r.imported, sharedTexture{tex, buf})
	}
	return tex, err
}

func (r *fakeRenderer) Begin(target render.Target, damage render.Region) (render.Pass, error) {
	for _, s := range r.imported {
		if err := s.tex.Update(image.Rectangle{Max: s.tex.Size()}, s.buf.Stride, s.buf.Pix); err != nil {
			return nil, err
		}
	}
	return r.Renderer.Begin(target, damage)
}

func (r *fakeRenderer) MainDevice() (uint64, error) { return 0, r.mainError }

func (r *fakeRenderer) NativeFences() bool { return r.native }

func (r *fakeRenderer) SyncFile() (int, error) {
	r.world.log = append(r.world.log, r.name+" fence")
	return unix.MemfdCreate("fence", unix.MFD_CLOEXEC)
}

func (r *fakeRenderer) WaitFence(fd int) error {
	r.world.log = append(r.world.log, r.name+" wait")
	return unix.Close(fd)
}

func (r *fakeRenderer) Finish() error {
	r.world.log = append(r.world.log, r.name+" finish")
	return nil
}

func fakeStack(world *blitWorld, name string, native bool) *gbmAllocator {
	return &gbmAllocator{
		dev: &fakeDevice{}, path: "/dev/dri/" + name, gbm: fakeGBM{world},
		gpu:      &fakeRenderer{Renderer: soft.New(), world: world, name: name, native: native, mainError: errors.New("this fake opens no Vulkan device")},
		closeGPU: func() error { return nil },
	}
}

func TestABlitShowsTheFrameThePrimaryDrewWhicheverRendererEachCardRuns(t *testing.T) {
	size := image.Pt(16, 16)
	for _, c := range []struct {
		name           string
		primary, local bool
		want           []string
	}{
		{"both have fences", true, true, []string{"primary fence", "local wait"}},
		{"only the primary has fences", true, false, []string{"primary finish"}},
		{"only the local card has fences", false, true, []string{"primary finish"}},
		{"neither has fences", false, false, []string{"primary finish"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			world := &blitWorld{memory: map[int]*soft.Buffer{}}
			a := &blitAllocator{primary: fakeStack(world, "primary", c.primary), local: fakeStack(world, "local", c.local)}
			if err := a.probe(); err != nil {
				t.Fatal(err)
			}
			a.local.gpu.(*fakeRenderer).imported = nil
			s, err := a.share(size, scanoutFormat)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := s.release(); err != nil {
					t.Error(err)
				}
			})
			p, err := a.primary.gpu.Begin(s.target, render.Region{{Max: size}})
			if err != nil {
				t.Fatal(err)
			}
			p.FillRect(image.Rectangle{Max: size}, render.Color{R: 0xff, A: 0xff}, render.Mask{})
			if err := p.End(); err != nil {
				t.Fatal(err)
			}
			dst := soft.NewBuffer(size.X, size.Y)
			if err := a.blit(s, dst, size); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(world.log, c.want) {
				t.Errorf("handed the frame over with %v, want %v", world.log, c.want)
			}
			if got := [4]byte(dst.Pix[:4]); got != [4]byte{0, 0, 0xff, 0xff} {
				t.Errorf("the local card shows %v, want the primary's red", got)
			}
		})
	}
}

func TestASecondaryStaysOnTheFirstStackItsCardRuns(t *testing.T) {
	world := &blitWorld{memory: map[int]*soft.Buffer{}}
	primary := &Backend{alloc: fakeStack(world, "primary", true)}
	dev := kdrm.FromFD(-1, "/dev/dri/card1")
	vulkanStack := namedStack{"Vulkan", func(*kdrm.Device) (*gbmAllocator, error) { return fakeStack(world, "local", true), nil }}
	noVulkan := namedStack{"Vulkan", func(*kdrm.Device) (*gbmAllocator, error) { return nil, errors.New("no Vulkan driver") }}
	glesStack := namedStack{"GLES", func(*kdrm.Device) (*gbmAllocator, error) { return fakeStack(world, "local", true), nil }}
	noGLES := namedStack{"GLES", func(*kdrm.Device) (*gbmAllocator, error) { return nil, errors.New("no EGL vendor") }}
	unlinear := fakeStack(world, "primary", true)
	unlinear.gpu.(*fakeRenderer).unlinear = true
	for _, c := range []struct {
		name      string
		primary   *Backend
		stacks    []namedStack
		canRender bool
		wantBlit  bool
		want      []string
	}{
		{"Vulkan on both", primary, []namedStack{vulkanStack, glesStack}, true, true, []string{"a GPU blit with Vulkan on card1"}},
		{"a card without Vulkan renders with GLES", primary, []namedStack{noVulkan, glesStack}, true, true, []string{"a GPU blit with GLES on card1"}},
		{"a card that renders with nothing takes the CPU copy", primary, []namedStack{noVulkan, noGLES}, true, false, []string{"a CPU copy", "no Vulkan driver", "no EGL vendor"}},
		{"a card without a render node takes the CPU copy", primary, []namedStack{vulkanStack}, false, false, []string{"a CPU copy", "no render node"}},
		{"GPUs that share no buffer take the CPU copy", &Backend{alloc: unlinear}, []namedStack{vulkanStack}, true, false, []string{"a CPU copy", "share no buffer", "renders into no linear buffer", "opens no Vulkan device"}},
		{"a primary that is no GPU stack takes the CPU copy", &Backend{alloc: &copyAllocator{primary: soft.New()}}, []namedStack{vulkanStack}, true, false, []string{"a CPU copy", "shares no buffers"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			alloc, how := secondaryAllocator(dev, c.primary, c.stacks, c.canRender)
			if _, blit := alloc.(*blitAllocator); blit != c.wantBlit {
				t.Errorf("%T for %q", alloc, how)
			}
			for _, want := range c.want {
				if !strings.Contains(how, want) {
					t.Errorf("%q does not say %q", how, want)
				}
			}
		})
	}
}

func TestStacksFollowTheRequestedRenderer(t *testing.T) {
	names := func(opts Options) []string {
		var out []string
		for _, s := range stacksFor(opts) {
			out = append(out, s.name)
		}
		return out
	}
	if got := names(Options{}); !slices.Equal(got, []string{"GLES"}) {
		t.Errorf("a GLES session's secondaries try %v", got)
	}
	if got := names(Options{Vulkan: true}); !slices.Equal(got, []string{"Vulkan", "GLES"}) {
		t.Errorf("a Vulkan session's secondaries try %v", got)
	}
}
