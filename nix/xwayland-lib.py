import json
import math
import re
import time

TABLET_MAX = 0x7FFF
TABLET_OUTPUT = "Virtual-1"


def as_alice(command):
    return params["as_alice"].format(command)


def in_x(command):
    return as_alice(f"DISPLAY=:0 {command}")


def windows():
    return json.loads(machine.succeed(as_alice("lys-wmctl windows")))


def outputs():
    return json.loads(machine.succeed(as_alice("lys-wmctl outputs")))


def x_windows():
    lines = machine.succeed(in_x(params["xwindows"])).splitlines()
    return [w for w in map(json.loads, lines) if w["viewable"]]


def device(v, scale):
    return math.floor(v * scale + 0.5)


def x_origin(output, largest):
    return device(output["rect"]["x"], largest), device(output["rect"]["y"], largest)


def nearest(outs, x, y):
    def distance(o):
        r = o["rect"]
        qx = min(max(x, r["x"]), r["x"] + r["width"] - 1)
        qy = min(max(y, r["y"]), r["y"] + r["height"] - 1)
        return math.hypot(qx - x, qy - y)

    return min(outs, key=distance)


def to_x(rect):
    outs = outputs()
    largest = max(o["scale"] for o in outs)
    o = nearest(outs, rect["x"], rect["y"])
    ox, oy = x_origin(o, largest)
    k = o["scale"]
    return {
        "x": ox + device(rect["x"] - o["rect"]["x"], k),
        "y": oy + device(rect["y"] - o["rect"]["y"], k),
        "width": device(rect["width"], k),
        "height": device(rect["height"], k),
    }


def scanout(name, device=""):
    shot = machine.out_dir / f"{name}.ppm"
    machine.send_monitor_command(f"screendump {shot} {device}".strip())
    data = shot.read_bytes()
    header = re.match(rb"P6\s+(\d+)\s+(\d+)\s+255\s", data)
    assert header, f"{name}: QEMU did not dump an 8-bit PPM"
    return int(header[1]), int(header[2]), data[header.end() :]


def wait_drawn(x_window, what):
    def colours():
        width, height, pixels = scanout("drawn")
        left, top = max(x_window["x"], 0), max(x_window["y"], 0)
        right, bottom = min(x_window["x"] + x_window["width"], width), min(x_window["y"] + x_window["height"], height)
        return len({pixels[3 * (y * width + x) : 3 * (y * width + x) + 3] for y in range(top, bottom, 4) for x in range(left, right, 4)}) >= 8

    wait_for(f"{what} drawn", colours, 120)


def launch(name):
    machine.succeed(as_alice(f"lys-wmctl dispatch exec {params['launchers'][name]}"))


def assert_physical_root():
    outs = outputs()
    want = [sum(o["mode"]["width"] for o in outs), max(o["mode"]["height"] for o in outs)]
    root = [int(v) for v in machine.succeed(in_x("xwininfo -root") + " | awk '/Width:|Height:/ {print $2}'").split()]
    assert root == want, f"Xwayland's root is {root}, want the outputs side by side in physical pixels {want}"


def x_geometry(w):
    return {k: w[k] for k in ("x", "y", "width", "height")}


def assert_unscaled(window, x_window):
    want, got = to_x(window["rect"]), x_geometry(x_window)
    assert got == want, f"{window['title']!r} is at {got} in X, want {want} for its logical {window['rect']}"


def wait_for(what, condition, timeout=120):
    deadline = time.monotonic() + timeout
    while True:
        found = condition()
        if found:
            return found
        if time.monotonic() > deadline:
            raise AssertionError(f"timed out waiting for {what}")
        time.sleep(1)


def window_titled(title, app_id=None):
    for w in windows():
        if w["title"] == title and (app_id is None or w["app_id"] == app_id):
            return w
    return None


def x_window_titled(title):
    found = [w for w in x_windows() if w["title"] == title and not w["override"]]
    return found[0] if len(found) == 1 else None


def qmp(*events):
    machine.qmp_client.send("input-send-event", {"events": list(events)})


def pointer_to(x, y):
    outs = outputs()
    o = next(o for o in outs if o["name"] == TABLET_OUTPUT)
    ox, oy = x_origin(o, max(p["scale"] for p in outs))
    width, height = o["mode"]["width"], o["mode"]["height"]
    assert ox <= x < ox + width and oy <= y < oy + height, f"the tablet reaches only {TABLET_OUTPUT}, not X {x},{y}"
    qmp(
        {"type": "abs", "data": {"axis": "x", "value": round((x - ox) * TABLET_MAX / width)}},
        {"type": "abs", "data": {"axis": "y", "value": round((y - oy) * TABLET_MAX / height)}},
    )
    time.sleep(0.3)


def pointer_by(dx, dy):
    machine.succeed(as_alice(f"WAYLAND_DISPLAY={display} {params['wlrctl']} pointer move {dx} {dy}"))
    time.sleep(0.3)


def button(down, which="left"):
    qmp({"type": "btn", "data": {"down": down, "button": which}})
    time.sleep(0.05)


def key(qcode, down):
    qmp({"type": "key", "data": {"down": down, "key": {"type": "qcode", "data": qcode}}})
    time.sleep(0.05)


def click(x, y, times=1):
    pointer_to(x, y)
    for _ in range(times):
        button(True)
        button(False)


def drag(x0, y0, dx, dy, modifier="meta_l", steps=10):
    pointer_to(x0, y0)
    key(modifier, True)
    button(True)
    for i in range(1, steps + 1):
        pointer_to(x0 + dx * i / steps, y0 + dy * i / steps)
    button(False)
    key(modifier, False)


def x_pointer():
    fields = dict(f.split(":") for f in machine.succeed(in_x("xdotool getmouselocation")).split())
    return int(fields["x"]), int(fields["y"])


def dump(logs):
    machine.screenshot("failure")
    for log in logs:
        print(machine.execute(f"tail -40 {log}")[1])
    print(machine.execute("journalctl -b --no-pager -t lys-wm | grep -v -e xkbcomp -e '> ' | tail -60")[1])
    print(machine.execute(as_alice("lys-wmctl windows") + " | jq -c '.[] | {id, app_id, title, rect, floating, focused}'")[1])
    print(machine.execute(in_x(params["xwindows"]))[1])
