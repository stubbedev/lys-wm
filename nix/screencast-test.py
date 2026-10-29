import json
import re
import shlex
import time

ANIMATION = "presentation-shm"
ANIMATION_PREVIEW = "ff0000"
OUTPUT = "Virtual-1"
OUTPUT_PIXELS = (1280, 800)
OUTPUT_SIZE = "x".join(map(str, OUTPUT_PIXELS))
MONITOR = f"Monitor: {OUTPUT}"
WINDOW = f"Window: {ANIMATION}"
KEYS = {MONITOR: "m", WINDOW: "w"}
ABS_MAX = 0x7FFF
CURSOR_BOX = 64
PICKER_PANEL = "262638"
PICKER_PROBE = (630, 630, 20, 10)
PICKER_PAGE = (0, 260, 1280, 330)
PICKER_PAGE_CENTER = (640, 420)
PICKER_OUTPUTS_TAB = (560, 228)
PICKER_APPEARS_S = 60
SHARE_FIELDS = re.compile(r"^shared (\d+x\d+) frames (\d+) seconds ([\d.]+) fps ([\d.]+) gap (\d+) distinct (\d+) background ([\d.]+)")


def as_alice(command):
    return params["as_alice"].format(command)


def windows():
    return json.loads(machine.succeed(as_alice("lys-wmctl windows")))


def launch(name):
    machine.succeed(as_alice(f"lys-wmctl dispatch exec {params['launchers'][name]}"))


def dump(*logs):
    print(machine.execute(as_alice("lys-wmctl windows") + " | jq -c '.[] | {id, app_id, title, rect}'")[1])
    for log in logs:
        print(machine.execute(f"tail -60 /tmp/{log}.log")[1])
    print(machine.execute(f"journalctl -b --no-pager _UID=1000 | grep -e {params['portal_log']} -e pipewire | tail -150")[1])


def chooser_runs_command():
    return f"journalctl -b --no-pager _UID=1000 | grep -c {shlex.quote(params['chooser_ran'])} || true"


def chooser_runs():
    return int(machine.succeed(chooser_runs_command()).strip())


def choose(label):
    if params["portal"] != "wlr":
        return 0
    machine.succeed(f"printf '%s\\n' {shlex.quote(label)} > /tmp/share-pick")
    return chooser_runs()


def pointer_events(events):
    machine.qmp_client.send("input-send-event", {"events": events})


def move_pointer(x, y):
    width, height = OUTPUT_PIXELS
    pointer_events([
        {"type": "abs", "data": {"axis": "x", "value": x * ABS_MAX // (width - 1)}},
        {"type": "abs", "data": {"axis": "y", "value": y * ABS_MAX // (height - 1)}},
    ])


def press(down):
    pointer_events([{"type": "btn", "data": {"down": down, "button": "left"}}])


def click(x, y, then=None):
    move_pointer(x, y)
    machine.sleep(0.5)
    press(True)
    machine.sleep(0.2)
    press(False)
    if then is not None:
        move_pointer(*then)
    machine.sleep(0.2)


def locate(color, x, y, width, height):
    status, out = machine.execute(as_alice(f"WAYLAND_DISPLAY={display} grim -g \"{x},{y} {width}x{height}\" -t ppm - | {params['screencast']} locate {color}"))
    if status != 0:
        return None
    cx, cy, count = map(int, out.split())
    return x + cx, y + cy, count


def picker_shown():
    found = locate(PICKER_PANEL, *PICKER_PROBE)
    return found is not None and found[2] == PICKER_PROBE[2] * PICKER_PROBE[3]


def pick_in_picker(label):
    if label == MONITOR:
        click(*PICKER_OUTPUTS_TAB)
        machine.sleep(2)
        click(*PICKER_PAGE_CENTER, then=POINTER_REST)
        return
    found = locate(ANIMATION_PREVIEW, *PICKER_PAGE)
    assert found is not None, f"the share picker shows no card for {label}"
    click(found[0], found[1], then=POINTER_REST)


def answer(label, ran, done, confirm):
    if params["portal"] == "wlr":
        if confirm:
            machine.wait_until_succeeds(f"[ $({chooser_runs_command()}) -gt {ran} ]", timeout=120)
            machine.sleep(3)
            machine.send_key("ret")
        return
    answered = 0
    deadline = time.monotonic() + PICKER_APPEARS_S
    while not done():
        if time.monotonic() > deadline:
            raise AssertionError(f"no share picker for {label} within {PICKER_APPEARS_S} s" if answered == 0 else f"sharing {label} did not finish after the pick")
        if picker_shown():
            machine.sleep(2)
            pick_in_picker(label)
            answered += 1
            if answered == 1:
                if confirm:
                    machine.sleep(3)
                    machine.send_key("ret")
                deadline = time.monotonic() + params["seconds"] + PICKER_APPEARS_S
        machine.sleep(1)


def capture(name, source, cursor, label):
    ran = choose(label)
    machine.succeed(as_alice(f"{params['background']} {name} {params['screencast']} capture {source} {cursor} 10 /tmp/{name}"))
    try:
        answer(label, ran, lambda: machine.execute(f"test -s /tmp/{name}.status")[0] == 0, False)
        machine.wait_until_succeeds(f"test -s /tmp/{name}.status", timeout=300)
        print(machine.succeed(f"cat /tmp/{name}.log"))
        machine.succeed(f"grep -qx 0 /tmp/{name}.status")
    except Exception:
        print(machine.execute(f"cat /tmp/{name}.log")[1])
        dump("animation")
        raise


def check_cursor(hidden, embedded, rect, px, py):
    print(machine.succeed(as_alice(f"{params['screencast']} cursor /tmp/{hidden} /tmp/{embedded} {rect['x']} {rect['y']} {rect['width']} {rect['height']} {px} {py}")))


def share(app, label, confirm):
    before = {w["id"] for w in windows()}
    launch(app)
    try:
        machine.wait_until_succeeds(as_alice("lys-wmctl windows") + " | jq -e 'any(.[]; .title | startswith(\"waiting\"))'", timeout=600)
    except Exception:
        dump(app)
        raise
    machine.sleep(5)
    [shown] = [w for w in windows() if w["id"] not in before]
    ran = choose(label)
    machine.send_key(KEYS[label])
    finished = as_alice("lys-wmctl windows") + f" | jq -e 'any(.[]; .id == {shown['id']} and (.title | test(\"^(shared|failed)\")))'"
    try:
        answer(label, ran, lambda: machine.execute(finished)[0] == 0, confirm)
        machine.wait_until_succeeds(finished, timeout=params["seconds"] + 300)
    except Exception:
        dump(app)
        raise
    finally:
        machine.screenshot(f"{app}-{KEYS[label]}")
    title = next(w for w in windows() if w["id"] == shown["id"])["title"]
    print(f"{app} shared {label}: {title}")
    fields = SHARE_FIELDS.match(title)
    if fields is None:
        dump(app)
        raise AssertionError(f"{app} could not share {label}: {title}")
    size, _, seconds, _, gap, distinct, background = fields.groups()
    assert float(seconds) >= params["seconds"], f"{app} shared {label} for only {seconds} s"
    assert int(gap) <= params["max_gap_ms"], f"{app} stalled {gap} ms sharing {label}"
    assert int(distinct) >= 2, f"{app} shared {label} without live content"
    if label == MONITOR:
        assert size == OUTPUT_SIZE, f"{app} shared {OUTPUT} at {size}"
    else:
        assert size != OUTPUT_SIZE, f"{app} shared {label} at the output's size"
        assert float(background) <= 0.01, f"{app} shared {label} with {background} of it the desktop"
    machine.succeed(f"kill {shown['pid']}")
    machine.wait_until_fails(f"kill -0 {shown['pid']}", timeout=60)


launch("animation")
machine.wait_until_succeeds(as_alice("lys-wmctl windows") + " | jq -e 'length == 1'", timeout=30)
machine.sleep(3)
[animation] = windows()
rect = animation["rect"]
POINTER_REST = (rect["x"] + rect["width"] // 2, rect["y"] + rect["height"] * 3 // 4)
move_pointer(*POINTER_REST)
around = {"x": POINTER_REST[0] - CURSOR_BOX, "y": POINTER_REST[1] - CURSOR_BOX, "width": 2 * CURSOR_BOX, "height": 2 * CURSOR_BOX}

capture("monitor-hidden", "monitor", "hidden", MONITOR)
capture("monitor-embedded", "monitor", "embedded", MONITOR)
check_cursor("monitor-hidden", "monitor-embedded", around, *POINTER_REST)
capture("window-hidden", "window", "hidden", WINDOW)
capture("window-embedded", "window", "embedded", WINDOW)
check_cursor("window-hidden", "window-embedded", {**around, "x": around["x"] - rect["x"], "y": around["y"] - rect["y"]}, POINTER_REST[0] - rect["x"], POINTER_REST[1] - rect["y"])

machine.succeed(as_alice(f"mkdir -p /tmp/firefox && cp {params['firefox_prefs']} /tmp/firefox/user.js"))
for app, confirm in [("firefox", False), ("chromium", True), ("electron", False)]:
    for label in [MONITOR, WINDOW]:
        share(app, label, confirm)
