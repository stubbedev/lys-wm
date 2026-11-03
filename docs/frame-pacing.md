# Frame pacing

`lys-wm-pace` measures how smoothly lys-wm's animations reach the screen. It
boots lys-wm in a private runtime directory with its own config, plays every
animation through a real client and the IPC socket, and reports per step:

| Column | What it is |
| --- | --- |
| `frames` | Frames the compositor ran during the step |
| `empty` | Frames that found nothing to draw, as the tail of a spring does below a pixel a frame |
| `dropped` | Frames that failed or never reached the screen |
| `late` | Frames shown after the vblank they aimed at: the one after the frame started |
| `skipped` | Vblanks those late frames missed |
| `interval` | From one frame on screen to the next, while the first of them asked for the second |
| `cpu` | The frame on the compositor's thread, without any wait for the GPU |
| `gpu` | From submitting the frame to the GPU finishing it: the time its fence took to signal, or the wait, on a stack without fences |
| `latency` | From the frame starting to it being on screen |

Times read p50 / p95 / p99 / worst, in milliseconds. `-json <file>` writes the
same summaries with every frame.

The steps cover opening and closing a window, moving one on a spring and
interrupting that, switching workspaces, a three-finger swipe following the
fingers and its release, the genie, and glass: over a client redrawing every
frame, over nothing moving (which must draw nothing), and sliding with a
workspace, and the overview with 10 and 30 overlapping floating windows, by action
and scrubbed by three fingers. Swipes come from the headless backend's fake touchpad, through
`lys-wm -headless-swipes`; `-backend drm` has no fingers to fake, and says it did
not measure them.

## Running it

```sh
just pace                         # headless, Vulkan, on this machine's GPU
just pace headless gles
just pace headless vulkan 30 "-json report.json -scenarios move,genie"
just pace drm vulkan              # real page flips, from a text console
just docker pace                  # headless on the host's GPU, from a container
```

`just pace` runs no tests first, so the hardware tests failing never blocks a
pacing number. `just hardware` runs them first and stops when they fail.

`headless` renders on a real GPU through its render node and leaves the display
alone, but its clock is a timer: vsync is simulated, and it latches frames as `drm`
does, half a refresh before each tick until a frame earns more, up to the whole
refresh. Only `-backend drm` has real present times; it takes
the display and needs a seat, so it refuses to run with `WAYLAND_DISPLAY`,
`DISPLAY` or a graphical `XDG_SESSION_TYPE` set. The report says which clock it
used.

A software rasteriser is refused, naming the driver it found, because its
numbers say nothing about a GPU. `-allow-software` accepts it for relative
numbers and hot spots, and the report then says so on its second line and sets
`real_gpu: false`. `-cpu-profile <file>` adds a Go CPU profile of lys-wm.


## The gate

Late frames, frame rates and the time columns measure the machine as much as the
compositor, so they are reported and never fail anything. `-gate` fails on what
no load can change, counted per step and compared with the limits checked in as
`cmd/lys-wm-pace/baseline.json`, one set per renderer and class of device: `gles/gpu`,
`vulkan/gpu`, `gles/software` and `vulkan/software`. They differ for good reason: a
software rasteriser turns the overview's blur off by default, so its limits say nothing
about a GPU. Every step and metric over its limit is named in one run:

| Limit | What it counts | What it catches |
| --- | --- | --- |
| `draws_per_frame` | Scene ops in the worst composited frame | Drawing what is covered, or a pile drawn twice |
| `render_passes_per_frame` | `Renderer.Begin` calls since the last composited frame, at worst | A new offscreen pass on every frame |
| `filter_passes_per_frame` | Kawase passes since the last composited frame, at worst | Filtering inside the frame what should wait for a calm one |
| `filter_passes_per_run` | Kawase passes in one play of an overview step: glass filters for as long as it takes, which the clock decides | Filtering that never stops once a thumbnail is calm |
| `uploads_per_frame` | Texture creations and updates since the last composited frame, at worst | Uploading what did not change |
| `thumb_draws_per_settle` | Overview thumbnails drawn in one play of the step | Redrawing thumbnails whose windows did not damage |
| `damage_rects_per_frame` | Rects in the worst frame's damage or repaint region, never above the cap of 32 | Damage that outgrows its cap while it accumulates |
| `allocs_per_frame` | Most heap allocations between one frame's start and end: a median would move with how many of a step's frames fall in its costly part, which the load decides | An allocation added to a frame's path |
| `file_io_per_frame` | Median read and write syscalls in a frame, from `/proc/self/io` | A file touched on every frame |
| `rss_growth_kib` | Resident size at the end of the last round minus the second, per scenario, as pipelines and arenas fill on first use | A leak that repeats with the animation |

`/proc/self/io` counts the read and write families only: an ioctl or a socket
call is not in `file_io_per_frame`, and the kernel offers no other unprivileged
count. Every number is per frame or per play, not per second, so a machine too
busy to reach 60 Hz draws fewer frames that each cost the same.

The trace takes these with `runtime.ReadMemStats` and a read of `/proc/self/io`
at both ends of each frame, outside the `cpu` column.

`just pace-gate gles` and `just pace-gate vulkan` run the gate in the dev shell on
the software rasterisers, as CI does, and `just hardware` runs it on the GPU.
A change that means to cost more updates its limits and commits the difference:
`just pace-baseline-software` writes the software sets, and `just docker pace-baseline`
the GPU sets from the host's GPU, both into the checkout, with a margin of a quarter over what
they measured.

## The trace

`lys-wm -frame-trace <file>` is what the tool reads: a JSON header, then one JSON
line per frame. Without the flag the compositor keeps one nil pointer and takes
no time, fence or file for it. With it, lines are written in chunks from another
goroutine, so the compositor's thread does no file I/O. Frames are fenced with
the same sync files KMS waits on, which costs nothing on a stack that has them; a
stack without them is waited for between submitting and presenting, which is
reported as `gpu_source: wait` and paces the frame the way a page flip would.
