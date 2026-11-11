# Frame pacing

`lys-wm-pace` measures how smoothly animations reach the screen. It starts
lys-wm in a private runtime directory with its own config, plays every
animation through a real client and the IPC socket, and reports per step.

## Running it

```sh
just docker pace                  # headless, Vulkan, on the host's GPU in a container
just pace drm vulkan              # real page flips, from a text console
just pace drm gles 30 "-json report.json -scenarios move,genie"
```

`just pace` refuses the headless backend outside the container, where it would render on the GPU beside the session; with `drm` it takes the display and runs from a text console. It runs no tests first; `just docker hardware` runs the hardware tests,
then pacing, and stops if the tests fail.

| Flag | Default | Effect |
| --- | --- | --- |
| `-backend` | `headless` | `headless` (simulated 60 Hz clock on a real GPU) or `drm` (takes the display; refuses to run with `WAYLAND_DISPLAY`, `DISPLAY` or a graphical `XDG_SESSION_TYPE`) |
| `-renderer` | `vulkan` | `gles` or `vulkan` |
| `-compositor` | next to `lys-wm-pace`, else `PATH` | The lys-wm binary |
| `-rounds` | `10` | Plays per animation |
| `-scenarios` | all | Comma-separated scenario names |
| `-json <file>` | | Write the report with every frame |
| `-cpu-profile <file>` | | Write a Go CPU profile of lys-wm |
| `-allow-software` | off | Accept a software rasteriser; the report marks it and sets `real_gpu: false` |
| `-gate` | off | Fail when a step exceeds `cmd/lys-wm-pace/baseline.json` |
| `-write-baseline <file>` | | Write limits for this renderer and device class, derived from this run |

The headless clock latches frames as `drm` does: half a refresh before each
tick, extended up to a whole refresh for frames that need it. A frame requested
with less time left than a typical render waits for the next latch. Only `drm`
has real present times; the report names the clock used.

Scenarios cover window open and close, spring moves and interruption, resize,
workspace switches, three-finger swipes (follow and release), the genie,
glass over busy, still and sliding content, translucent tiles sliding and
resizing, and the overview with 10 and 30 floating windows by action and by
swipe. On a real GPU, `scanout-scaled-fullscreen` and
`scanout-windowed-video` check direct scanout on scaling primary and overlay
planes (`lys-wm -headless-scanout`); they are skipped with
`-allow-software`. Swipes use the headless backend's fake touchpad
(`lys-wm -headless-swipes`) and are skipped on `drm`.

## Report

Times are p50 / p95 / p99 / worst in milliseconds.

| Column | Meaning |
| --- | --- |
| `frames` | Frames the compositor ran |
| `empty` | Frames with nothing to draw |
| `dropped` | Frames that failed or never reached the screen |
| `late` | Frames shown after the vblank they aimed at |
| `skipped` | Vblanks the late frames missed |
| `interval` | Between consecutive frames on screen, when the first requested the second |
| `cpu` | Compositor-thread time, excluding GPU waits |
| `gpu` | Submit to GPU completion (fence signal, or the wait without fences) |
| `latency` | Frame start to on screen |
| `sched` | Worst runnable-goroutine wait for a processor (`runtime/metrics`) |
| `gc` | Worst GC stop-the-world pause (`runtime/metrics`) |

The cost table also reports the worst frame's `targets` (render targets
created), `filter px`, `repaint px` and `copied bytes` (capture bytes copied
to clients). A final table gives, for overview open and close steps, the time
from the action to the first frame within 5% of the target and to the first
frame at rest (`-` if not reached in the step).

## The gate

Timing depends on the machine, so it is reported and never gated. `-gate`
checks counts per frame or per play against `cmd/lys-wm-pace/baseline.json`,
which holds one set per renderer and device class: `gles/gpu`, `vulkan/gpu`,
`gles/software`, `vulkan/software`. Every step and metric over its limit is
reported in one run.

| Limit | Counts | Catches |
| --- | --- | --- |
| `draws_per_frame` | Scene ops in the worst composited frame | Drawing covered content or drawing twice |
| `render_passes_per_frame` | `Renderer.Begin` calls per composited frame, worst | A new offscreen pass every frame |
| `filter_passes_per_frame` | Kawase passes per composited frame, worst | Filtering mid-animation |
| `filter_passes_per_run` | Kawase passes in one overview play | Filtering that never settles |
| `render_passes_per_scanout_frame` | `scanout-` steps: `Renderer.Begin` calls in any frame, limit 0 | Compositing what should scan out |
| `render_passes_after_first_frame`, `filter_passes_after_first_frame` | `glass-tiles` steps: passes in any frame after the first, limits 1 and 0 | Tiles refiltering the shared background chain every frame |
| `filter_pixels_per_frame`, `repaint_pixels_per_frame` | `glass-over-busy`: pixels filtered within damage and repainted, worst frame | Refiltering or repainting the whole window instead of the damaged band plus blur reach |
| `uploads_per_frame` | Texture creations and updates per composited frame, worst | Re-uploading unchanged content |
| `gpu_waits_per_frame` | Loop waits on the GPU per composited frame (fence waits on Vulkan, `glFinish` on GLES), worst, no slack | Stalling the loop on the GPU |
| `submits_per_frame` | GPU submissions per composited frame (Vulkan queue submits, GLES flushes), worst, no slack | Extra submissions per pass |
| `thumb_draws_per_settle` | Overview thumbnails drawn per play | Redrawing undamaged thumbnails |
| `damage_rects_per_frame` | Rects in the worst frame's damage or repaint region (cap 32) | Damage outgrowing its cap |
| `allocs_per_frame` | Heap allocations in the costliest frame of a play, from the play where that is lowest | Allocation added to a per-play path |
| `file_io_per_frame` | Median read/write syscalls per frame on the loop thread (`/proc/thread-self/io`; ioctls and socket calls are not counted) | File I/O every frame |
| RSS growth (1024 KiB, fixed) | Smallest per-round rise in resident size after 2 warm-up rounds; needs at least 5 rounds | A leak that repeats with the animation |

`just pace-gate gles` and `just pace-gate vulkan` run the gate on the dev
shell's software rasterisers, as CI does; `just docker hardware` runs it on the
GPU.
A change that is meant to cost more updates the limits and commits them:
`just pace-baseline-software` writes the software sets,
`just docker pace-baseline` the GPU sets from the host's GPU. Limits are the measured value
plus a quarter.

## The trace

`lys-wm -frame-trace <file>` writes the input `lys-wm-pace` reads: a JSON
header, then one JSON line per frame. The `debug trace` action
([IPC](./ipc#debugging-a-running-session)) writes the same format from a
running session.

- Without the flag tracing costs nothing.
- Lines are written in chunks from another goroutine; the compositor thread
  does no file I/O.
- Frames are fenced with the sync files KMS waits on. Without them the frame
  is waited for between submit and present, reported as `gpu_source: wait`.
- Allocation and I/O counts come from `runtime.ReadMemStats` and
  `/proc/self/io` at both ends of each frame, outside the `cpu` column.
