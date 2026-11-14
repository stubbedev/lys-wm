package vulkan

import "github.com/stubbedev/lys-wm/internal/gpu"

var YCbCrOf = ycbcrOf

func OpaqueRuns(r *Renderer) uint64 { return r.d.OpaqueRuns() }

func KeptPlanes(r *Renderer) []gpu.Plane {
	var planes []gpu.Plane
	for t := range r.targets {
		if t.kept != nil {
			planes = append(planes, t.kept.Planes...)
		}
	}
	for l := range r.linears {
		if l.kept != nil {
			planes = append(planes, l.kept.Planes...)
		}
	}
	return planes
}

func Warm(r *Renderer, t *Target) error { return r.warm(t) }
