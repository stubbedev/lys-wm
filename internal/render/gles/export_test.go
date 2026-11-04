package gles

func DisableUnpackBuffer(r *Renderer) { r.es3 = false }

func DrawnOps(r *Renderer) uint64 { return r.drawnOps }

func OpaqueRuns(r *Renderer) uint64 { return r.opaqueRuns }
