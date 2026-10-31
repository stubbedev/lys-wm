import json
import re
import shlex
import time

ANIMATION = "presentation-shm"
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
PICKER_APPEARS_S = 60
PAGE_LOADS_S = 300
DIALOG_READY_S = 60
CAST_LINKED = "jq -e '([.[] | select(.type == \"PipeWire:Interface:Node\" and .info.props[\"media.class\"] == \"Stream/Input/Video\") | .id]) as $viewers | any(.[]; .type == \"PipeWire:Interface:Link\" and .info.state == \"active\" and (.info[\"input-node-id\"] as $node | $viewers | index($node)))'"
PICKER_SERVICE = "dev.stubbe.lys.SharePicker1"
SOURCE_MONITOR = 1
SOURCE_WINDOW = 2
PICKER_PAGES = [("Windows", SOURCE_WINDOW), ("Outputs", SOURCE_MONITOR), ("Region", SOURCE_MONITOR)]
PICKER_OPENS_ON = "Windows"
SELECT_SOURCES_TYPES = re.compile(r'string "types"\s+variant\s+uint32 (\d+)')
SHARE_FIELDS = re.compile(r"^shared (\d+x\d+) frames (\d+) seconds ([\d.]+) fps ([\d.]+) gap (\d+) distinct (\d+) background ([\d.]+)")


def as_alice(command):
    return params["as_alice"].format(command)


def windows():
    return json.loads(machine.succeed(as_alice("lys-wmctl windows")))


def launch(name):
    machine.succeed(as_alice(f"lys-wmctl dispatch exec {params['launchers'][name]}"))


def cpu_times():
    fields = [int(v) for v in machine.succeed("grep '^cpu ' /proc/stat").split()[1:]]
    return fields[7], sum(fields)


def stolen(since=(0, 0)):
    steal, total = (now - then for now, then in zip(cpu_times(), since))
    return f"host stole {100 * steal / max(total, 1):.1f}% of the guest's CPU time"


def dump(*logs):
    print(f"{stolen()} since boot")
    print(machine.execute(as_alice("lys-wmctl windows") + " | jq -c '.[] | {id, app_id, title, rect}'")[1])
    for log in logs:
        print(machine.execute(f"tail -60 /tmp/{log}.log")[1])
        print(machine.execute(f"grep -n -B60 -m1 'Network service crashed' /tmp/{log}.log | cut -c1-260")[1])
    print(machine.execute(f"journalctl -b --no-pager _UID=1000 | grep -e {params['portal_log']} -e pipewire | tail -150")[1])


def chooser_runs_command():
    return f"journalctl -b --no-pager _UID=1000 | grep -c {shlex.quote(params['chooser_ran'])} || true"


def chooser_runs():
    return int(machine.succeed(chooser_runs_command()).strip())


def requested_types():
    log = machine.succeed("cat /tmp/select-sources.log")
    types = []
    for call in log.split("method call ")[1:]:
        found = SELECT_SOURCES_TYPES.search(call)
        types.append(int(found.group(1)) if found else SOURCE_MONITOR)
    return types


def choose(label):
    if params["portal"] != "wlr":
        return len(requested_types())
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


def locate(color, x, y, width, height):
    status, out = machine.execute(as_alice(f"WAYLAND_DISPLAY={display} grim -g \"{x},{y} {width}x{height}\" -t ppm - | {params['screencast']} locate {color}"))
    if status != 0:
        return None
    cx, cy, count = map(int, out.split())
    return x + cx, y + cy, count


def picker_shown():
    found = locate(PICKER_PANEL, *PICKER_PROBE)
    return found is not None and found[2] == PICKER_PROBE[2] * PICKER_PROBE[3]


def keys(*names):
    for name in names:
        machine.send_key(name)
        machine.sleep(0.3)


def pick_in_picker(label, ran):
    machine.wait_until_succeeds(f"[ $(grep -c '^method call ' /tmp/select-sources.log) -gt {ran} ]", timeout=PICKER_APPEARS_S)
    types = requested_types()[-1]
    pages = [name for name, source in PICKER_PAGES if types & source]
    wanted = "Outputs" if label == MONITOR else "Windows"
    assert wanted in pages, f"sharing {label} asked for source types {types:#x}, which the picker shows as {pages}"
    opened = PICKER_OPENS_ON if PICKER_OPENS_ON in pages else pages[0]
    keys(*["ctrl-pgdn"] * ((pages.index(wanted) - pages.index(opened)) % len(pages)))
    if label == WINDOW:
        shown = sorted(windows(), key=lambda w: w["id"])
        assert shown[0]["title"].startswith(f"{ANIMATION}:"), f"{ANIMATION} is not the oldest window, so not the last card: {[w['title'] for w in shown]}"
        keys(*["down"] * len(shown), *["right"] * len(shown))
    keys("ret")


def cast_linked():
    return machine.execute(as_alice("pw-dump") + " | " + CAST_LINKED)[0] == 0


def confirm_in_dialog(window_id):
    deadline = time.monotonic() + DIALOG_READY_S
    while True:
        window = next((w for w in windows() if w["id"] == window_id), None)
        if window is None:
            raise AssertionError(f"window {window_id} closed before its share dialog could be confirmed")
        linked = cast_linked()
        if window["focused"] and linked:
            break
        if time.monotonic() > deadline:
            machine.screenshot("confirm-dialog")
            print(machine.execute(as_alice("pw-dump") + " | jq -c '.[] | select(.type | test(\"Node|Link\")) | {id, type, state: .info.state, class: .info.props[\"media.class\"], app: .info.props[\"application.name\"], out: .info[\"output-node-id\"], in: .info[\"input-node-id\"]}'")[1])
            raise AssertionError(f"window {window_id} was {'' if window['focused'] else 'not '}focused and its share dialog's preview {'was' if linked else 'was not'} linked to a screen cast within {DIALOG_READY_S} s: {window}")
        machine.sleep(0.5)
    machine.send_key("ret")


def answer(label, ran, done, confirm):
    if params["portal"] == "wlr":
        if confirm:
            machine.wait_until_succeeds(f"[ $({chooser_runs_command()}) -gt {ran} ]", timeout=PICKER_APPEARS_S)
            confirm()
        return
    answered = 0
    deadline = time.monotonic() + PICKER_APPEARS_S
    while not done():
        if time.monotonic() > deadline:
            if answered == 0:
                machine.screenshot("no-picker")
                raise AssertionError(f"no share picker for {label} within {PICKER_APPEARS_S} s, after {len(requested_types()) - ran} SelectSources calls; windows: {[(w['title'], w['focused']) for w in windows()]}")
            raise AssertionError(f"sharing {label} did not finish after the pick")
        if picker_shown():
            machine.sleep(2)
            pick_in_picker(label, ran)
            answered += 1
            if answered == 1:
                if confirm:
                    confirm()
                deadline = time.monotonic() + params["seconds"] + PICKER_APPEARS_S
        machine.sleep(1)


def capture(name, source, cursor, label):
    ran = choose(label)
    machine.succeed(as_alice(f"{params['background']} {name} {params['screencast']} capture {source} {cursor} 10 /tmp/{name}"))
    try:
        answer(label, ran, lambda: machine.execute(f"test -s /tmp/{name}.status")[0] == 0, None)
        machine.wait_until_succeeds(f"test -s /tmp/{name}.status", timeout=300)
        print(machine.succeed(f"cat /tmp/{name}.log"))
        machine.succeed(f"grep -qx 0 /tmp/{name}.status")
    except Exception:
        print(machine.execute(f"cat /tmp/{name}.log")[1])
        dump("animation")
        raise


def check_cursor(hidden, embedded, rect, px, py):
    print(machine.succeed(as_alice(f"{params['screencast']} cursor /tmp/{hidden} /tmp/{embedded} {rect['x']} {rect['y']} {rect['width']} {rect['height']} {px} {py}")))


def share(app, label, confirms):
    before = {w["id"] for w in windows()}
    started = cpu_times()
    launch(app)
    try:
        machine.wait_until_succeeds(as_alice("lys-wmctl windows") + " | jq -e 'any(.[]; .title | startswith(\"waiting\"))'", timeout=PAGE_LOADS_S)
    except Exception as e:
        dump(app)
        crashed = machine.execute(f"grep -c 'Network service crashed' /tmp/{app}.log")[1].strip()
        raise AssertionError(f"{app} did not load the test page within {PAGE_LOADS_S} s; {stolen(started)} meanwhile; its network service crashed {crashed} times") from e
    machine.sleep(5)
    [shown] = [w for w in windows() if w["id"] not in before]
    ran = choose(label)
    machine.send_key(KEYS[label])
    confirm = (lambda: confirm_in_dialog(shown["id"])) if confirms else None
    finished = as_alice("lys-wmctl windows") + f" | jq -e 'any(.[]; .id == {shown['id']} and (.title | test(\"^(shared|failed)\")))'"
    try:
        answer(label, ran, lambda: machine.execute(finished)[0] == 0, confirm)
        machine.wait_until_succeeds(finished, timeout=params["seconds"] + PICKER_APPEARS_S)
    except Exception:
        dump(app)
        raise
    finally:
        machine.screenshot(f"{app}-{KEYS[label]}")
    title = next(w for w in windows() if w["id"] == shown["id"])["title"]
    print(f"{app} shared {label}: {title}; {stolen(started)} while it ran")
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


if params["portal"] != "wlr":
    machine.succeed(as_alice(f"{params['background']} select-sources {params['select_sources']}"))
    machine.wait_until_succeeds("pgrep -u alice -x dbus-monitor", timeout=30)
    machine.wait_until_succeeds(as_alice(f"busctl --user list --no-legend | grep -q {PICKER_SERVICE}"), timeout=PICKER_APPEARS_S)
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
for app, confirms in [("firefox", False), ("chromium", True), ("electron", False)]:
    for label in [MONITOR, WINDOW]:
        share(app, label, confirms)
