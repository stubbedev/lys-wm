import argparse
import subprocess
import sys

import dbus
import dbus.mainloop.glib
from gi.repository import GLib

SOURCES = {"monitor": 1, "window": 2}
CURSOR_MODES = {"hidden": 1, "embedded": 2}
BACKGROUND = bytes([0x2E, 0x1E, 0x1E])
CURSOR_REACH = 64
COLOR_TOLERANCE = 3


def start_cast(source, cursor_mode):
    dbus.mainloop.glib.DBusGMainLoop(set_as_default=True)
    bus = dbus.SessionBus()
    portal = bus.get_object("org.freedesktop.portal.Desktop", "/org/freedesktop/portal/desktop")
    screencast = dbus.Interface(portal, "org.freedesktop.portal.ScreenCast")
    sender = bus.get_unique_name()[1:].replace(".", "_")
    loop = GLib.MainLoop()
    outcome = {}
    tokens = iter(range(1, 100))

    def fail(message):
        outcome["error"] = message
        loop.quit()

    def request(name, handler, *args):
        token = f"lyswm{next(tokens)}"
        path = f"/org/freedesktop/portal/desktop/request/{sender}/{token}"

        def respond(code, results):
            print(f"screencast: {name} answered {code}", flush=True)
            if code != 0:
                fail(f"{name} answered {code} with {dict(results)}")
                return
            handler(results)

        bus.add_signal_receiver(respond, "Response", "org.freedesktop.portal.Request", path=path)
        args[-1]["handle_token"] = token
        getattr(screencast, name)(*args, reply_handler=lambda _: None, error_handler=lambda e: fail(f"{name}: {e}"))

    def started(results):
        streams = results.get("streams", [])
        if len(streams) != 1:
            fail(f"Start gave streams {streams}")
            return
        outcome["node"] = int(streams[0][0])
        loop.quit()

    def selected(_):
        request("Start", started, outcome["session"], "", dbus.Dictionary({}, signature="sv"))

    def created(results):
        outcome["session"] = results["session_handle"]
        options = dbus.Dictionary(
            {"types": dbus.UInt32(SOURCES[source]), "cursor_mode": dbus.UInt32(CURSOR_MODES[cursor_mode]), "multiple": False},
            signature="sv",
        )
        request("SelectSources", selected, outcome["session"], options)

    request("CreateSession", created, dbus.Dictionary({"session_handle_token": "lyswm"}, signature="sv"))
    GLib.timeout_add_seconds(120, lambda: fail("the portal never answered"))
    loop.run()
    if "error" in outcome:
        sys.exit(f"screencast: {outcome['error']}")
    remote = screencast.OpenPipeWireRemote(outcome["session"], dbus.Dictionary({}, signature="sv")).take()
    return remote, outcome["node"]


def read_frames(path):
    with open(f"{path}.size") as f:
        width, height, frames = (int(v) for v in f.read().split())
    size = width * height * 4
    with open(path, "rb") as f:
        captured = f.read()
    if size == 0 or len(captured) != frames * size:
        sys.exit(f"screencast: {len(captured)} bytes in {path} for {frames} frames of {width}x{height}")
    return width, height, [captured[at:at + size] for at in range(0, len(captured), size)]


def capture(args):
    remote, node = start_cast(args.source, args.cursor)
    result = subprocess.run(
        ["pw-frames", str(remote), str(node), str(args.frames), args.path],
        timeout=120,
        pass_fds=(remote,),
        capture_output=True,
        text=True,
    )
    if result.returncode != 0:
        sys.exit(f"screencast: pw-frames exited {result.returncode}: {result.stderr}")
    sizes = result.stdout.split("\n")[0].split()
    if len(sizes) != 2:
        sys.exit(f"screencast: pw-frames reported no size in {result.stdout!r}")
    with open(f"{args.path}.size", "w") as f:
        f.write(f"{sizes[0]} {sizes[1]} {args.frames}\n")
    width, height, frames = read_frames(args.path)
    distinct = len(set(frames))
    last = frames[-1]
    background = sum(1 for at in range(0, len(last), 4) if last[at:at + 3] == BACKGROUND) / (width * height)
    print(f"screencast: {args.source} stream node {node}, {width}x{height}, cursor {args.cursor}", flush=True)
    print(f"screencast: {distinct} distinct frames of {len(frames)}; {background:.3f} of the last is the desktop background", flush=True)
    if distinct < 2:
        sys.exit("screencast: the stream shows no live content")
    if args.source == "monitor" and not 0.1 <= background < 1:
        sys.exit("screencast: the last frame does not show the desktop and its windows")
    if args.source == "window" and background > 0.01:
        sys.exit("screencast: the last frame shows the desktop, not just the window")


def cursor(args):
    width, height, hidden = read_frames(args.hidden)
    embedded_width, embedded_height, embedded = read_frames(args.embedded)
    if (embedded_width, embedded_height) != (width, height):
        sys.exit(f"screencast: the hidden stream is {width}x{height}, the embedded one {embedded_width}x{embedded_height}")
    a, b = hidden[-1], embedded[-1]
    x0, y0 = max(args.x, 0), max(args.y, 0)
    x1, y1 = min(args.x + args.width, width), min(args.y + args.height, height)
    changed = [(x, y) for y in range(y0, y1) for x in range(x0, x1) if a[(y * width + x) * 4:(y * width + x) * 4 + 3] != b[(y * width + x) * 4:(y * width + x) * 4 + 3]]
    if not changed:
        sys.exit(f"screencast: the embedded cursor drew nothing in {x1 - x0}x{y1 - y0}+{x0}+{y0}")
    xs, ys = [x for x, _ in changed], [y for _, y in changed]
    box = (min(xs), min(ys), max(xs), max(ys))
    print(f"screencast: the embedded cursor changed {len(changed)} pixels in {box}, the pointer at {args.px},{args.py}", flush=True)
    if box[2] - box[0] > CURSOR_REACH or box[3] - box[1] > CURSOR_REACH:
        sys.exit("screencast: hidden and embedded frames differ by more than a cursor")
    if not (box[0] - CURSOR_REACH <= args.px <= box[2] + CURSOR_REACH and box[1] - CURSOR_REACH <= args.py <= box[3] + CURSOR_REACH):
        sys.exit("screencast: the embedded cursor is not where the pointer is")


def read_ppm(data):
    fields = data.split(maxsplit=4)
    if len(fields) != 5 or fields[0] != b"P6" or fields[3] != b"255":
        sys.exit(f"screencast: not an 8-bit PPM: {data[:20]!r}")
    width, height = int(fields[1]), int(fields[2])
    pixels = fields[4]
    if len(pixels) != width * height * 3:
        sys.exit(f"screencast: {len(pixels)} bytes of pixels for a {width}x{height} PPM")
    return width, height, pixels


def locate(args):
    width, height, pixels = read_ppm(sys.stdin.buffer.read())
    color = bytes.fromhex(args.color)
    found = [
        (at // 3 % width, at // 3 // width)
        for at in range(0, len(pixels), 3)
        if all(abs(pixels[at + i] - color[i]) <= COLOR_TOLERANCE for i in range(3))
    ]
    if not found:
        sys.exit(f"screencast: no #{args.color} in {width}x{height}")
    print(sum(x for x, _ in found) // len(found), sum(y for _, y in found) // len(found), len(found))


def main():
    parser = argparse.ArgumentParser()
    commands = parser.add_subparsers(required=True)
    cap = commands.add_parser("capture")
    cap.add_argument("source", choices=SOURCES)
    cap.add_argument("cursor", choices=CURSOR_MODES)
    cap.add_argument("frames", type=int)
    cap.add_argument("path")
    cap.set_defaults(run=capture)
    cur = commands.add_parser("cursor")
    cur.add_argument("hidden")
    cur.add_argument("embedded")
    for name in ("x", "y", "width", "height", "px", "py"):
        cur.add_argument(name, type=int)
    cur.set_defaults(run=cursor)
    loc = commands.add_parser("locate")
    loc.add_argument("color")
    loc.set_defaults(run=locate)
    args = parser.parse_args()
    args.run(args)


if __name__ == "__main__":
    main()
