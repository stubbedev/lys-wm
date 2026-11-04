package gles

func DisableUnpackBuffer(r *Renderer) { r.es3 = false }

func DrawnOps(r *Renderer) uint64 { return r.drawnOps }
