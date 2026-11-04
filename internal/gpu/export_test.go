package gpu

func PinnedLookups() uint64 { return pinned.lookups.Load() }
