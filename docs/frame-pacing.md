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
workspace. Swipes come from the headless backend's fake touchpad, through
`lys-wm -headless-swipes`; `-backend drm` has no fingers to fake, and says it did
not measure them.

## Running it

```sh
just pace                         # headless, Vulkan, on this machine's GPU
just pace renderer=gles
just pace rounds=30 args="-json report.json -scenarios move,genie"
just pace backend=drm             # real page flips, from a text console
just docker pace                  # headless on the host's GPU, from a container
```

`just pace` runs no tests first, so the hardware tests failing never blocks a
pacing number. `just hardware` runs them first and stops when they fail.

`headless` renders on a real GPU through its render node and leaves the display
alone, but its clock is a timer: vsync is simulated, and it latches frames half
a refresh before each tick. Only `-backend drm` has real present times; it takes
the display and needs a seat, so it refuses to run with `WAYLAND_DISPLAY`,
`DISPLAY` or a graphical `XDG_SESSION_TYPE` set. The report says which clock it
used.

A software rasteriser is refused, naming the driver it found, because its
numbers say nothing about a GPU. `-allow-software` accepts it for relative
numbers and hot spots, and the report then says so on its second line and sets
`real_gpu: false`. `-cpu-profile <file>` adds a Go CPU profile of lys-wm.
`-max-late-percent <n>` fails when any step presents more than that share of its
frames late; `just hardware` uses it.

## The trace

`lys-wm -frame-trace <file>` is what the tool reads: a JSON header, then one JSON
line per frame. Without the flag the compositor keeps one nil pointer and takes
no time, fence or file for it. With it, lines are written in chunks from another
goroutine, so the compositor's thread does no file I/O. Frames are fenced with
the same sync files KMS waits on, which costs nothing on a stack that has them; a
stack without them is waited for between submitting and presenting, which is
reported as `gpu_source: wait` and paces the frame the way a page flip would.
