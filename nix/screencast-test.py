import json
import re
import shlex

ANIMATION = "presentation-shm"
OUTPUT = "Virtual-1"
OUTPUT_PIXELS = (1280, 800)
OUTPUT_SIZE = "x".join(map(str, OUTPUT_PIXELS))
MONITOR = f"Monitor: {OUTPUT}"
WINDOW = f"Window: {ANIMATION}"
KEYS = {MONITOR: "m", WINDOW: "w"}
ABS_MAX = 0x7FFF
CURSOR_BOX = 64
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


def capture(name, source, cursor, label):
    choose(label)
    try:
        print(machine.succeed(as_alice(f"{params['screencast']} capture {source} {cursor} 10 /tmp/{name}")))
    except Exception:
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
    if confirm:
        machine.wait_until_succeeds(f"[ $({chooser_runs_command()}) -gt {ran} ]", timeout=120)
        machine.sleep(3)
        machine.send_key("ret")
    try:
        machine.wait_until_succeeds(as_alice("lys-wmctl windows") + f" | jq -e 'any(.[]; .id == {shown['id']} and (.title | test(\"^(shared|failed)\")))'", timeout=params["seconds"] + 300)
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
