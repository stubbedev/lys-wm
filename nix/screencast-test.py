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
CHOOSER_APPEARS_S = 60
ENCODER_ROUNDING = 1
BEFORE_OVERLAY = "/tmp/before-overlay.ppm"
NOT_DIMMED = 3
PAGE_LOADS_S = 300
DIALOG_READY_S = 60
CAST_LINKED = "jq -e '([.[] | select(.type == \"PipeWire:Interface:Node\" and .info.props[\"media.class\"] == \"Stream/Input/Video\") | .id]) as $viewers | any(.[]; .type == \"PipeWire:Interface:Link\" and .info.state == \"active\" and (.info[\"input-node-id\"] as $node | $viewers | index($node)))'"
PICKER_SERVICE = "dev.stubbe.lys.SharePicker1"
SOURCE_MONITOR = 1
SOURCE_WINDOW = 2
SELECT_SOURCES_TYPES = re.compile(r'string "types"\s+variant\s+uint32 (\d+)')
SELECT_SOURCES_RESTORES = re.compile(r'string "restore_data"')
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
    print(machine.execute("journalctl -b --no-pager -t lys-wm | grep -v -e xkbcomp -e 'Virtual modifier' -e 'Using 0' -e 'X11 cannot' -e 'keycode' | tail -40")[1])


def chooser_runs_command():
    return f"journalctl -b --no-pager _UID=1000 | grep -c {shlex.quote(params['chooser_ran'])} || true"


def chooser_runs():
    return int(machine.succeed(chooser_runs_command()).strip())


def select_sources_calls():
    return machine.succeed("cat /tmp/select-sources.log").split("method call ")[1:]


def requested_types(call):
    found = SELECT_SOURCES_TYPES.search(call)
    return int(found.group(1)) if found else SOURCE_MONITOR


def method_calls_command(log):
    return f"grep -c '^method call ' /tmp/{log}.log || true"


def method_calls(log):
    return int(machine.succeed(method_calls_command(log)).strip())


def choose(label):
    if params["portal"] != "wlr":
        screen_to(BEFORE_OVERLAY)
        return len(select_sources_calls()), method_calls("pick-window")
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


def screen_to(path):
    machine.succeed(as_alice(f"WAYLAND_DISPLAY={display} grim -t ppm {path}"))


def overlay_shown():
    status, out = machine.execute(as_alice(f"WAYLAND_DISPLAY={display} grim -t ppm - | {params['screencast']} dimmed {BEFORE_OVERLAY}"))
    if status not in (0, NOT_DIMMED):
        raise AssertionError(f"cannot tell whether the click-to-pick overlay shows: {out}")
    print(out.strip())
    return status == 0


def click_at(x, y):
    move_pointer(x, y)
    machine.sleep(0.3)
    for down in (True, False):
        pointer_events([{"type": "btn", "data": {"down": down, "button": "left"}}])
        machine.sleep(0.1)


def window_titled(prefix):
    [found] = [w for w in windows() if w["title"].startswith(prefix)]
    return found


def inside(rect, x, y):
    return rect["x"] <= x < rect["x"] + rect["width"] and rect["y"] <= y < rect["y"] + rect["height"]


def click_window(title_prefix):
    target = window_titled(title_prefix)
    r = target["rect"]
    x, y = r["x"] + r["width"] // 2, r["y"] + r["height"] * 3 // 4
    covering = [w for w in windows() if w["id"] != target["id"] and inside(w["rect"], x, y)]
    assert not covering, f"{x},{y} in {title_prefix} is under {covering}, so a click there picks another window"
    click_at(x, y)


def park_pointer_off_windows():
    top = min(w["rect"]["y"] for w in windows())
    assert top > 0, f"no window leaves the top of {OUTPUT} free to park the pointer off every window: {windows()}"
    move_pointer(OUTPUT_PIXELS[0] // 2, top // 2)


def pick_in_overlay(label, ran):
    selects, picks = ran
    machine.wait_until_succeeds(f"[ $({method_calls_command('select-sources')}) -gt {selects} ]", timeout=CHOOSER_APPEARS_S)
    call = select_sources_calls()[selects]
    types = requested_types(call)
    wanted = SOURCE_MONITOR if label == MONITOR else SOURCE_WINDOW
    assert types & wanted, f"sharing {label} asked for source types {types:#x}"
    if not types & SOURCE_WINDOW or SELECT_SOURCES_RESTORES.search(call):
        return selects + 1, picks
    park_pointer_off_windows()
    try:
        machine.wait_until_succeeds(f"[ $({method_calls_command('pick-window')}) -gt {picks} ]", timeout=CHOOSER_APPEARS_S)
        deadline = time.monotonic() + CHOOSER_APPEARS_S
        while not overlay_shown():
            if time.monotonic() > deadline:
                raise AssertionError(f"the portal asked for the click-to-pick overlay sharing {label}, but none dimmed the desktop within {CHOOSER_APPEARS_S} s; the request: {call}")
            machine.sleep(1)
    except Exception:
        print(f"the request left unanswered: {call}")
        machine.screenshot("no-overlay")
        raise
    if label == WINDOW:
        click_window(f"{ANIMATION}:")
    else:
        machine.send_key("ret")
    return selects + 1, picks + 1


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
            machine.wait_until_succeeds(f"[ $({chooser_runs_command()}) -gt {ran} ]", timeout=CHOOSER_APPEARS_S)
            confirm()
        return
    ran = pick_in_overlay(label, ran)
    if confirm:
        confirm()
    deadline = time.monotonic() + params["seconds"] + CHOOSER_APPEARS_S
    while not done():
        if len(select_sources_calls()) > ran[0]:
            ran = pick_in_overlay(label, ran)
        elif time.monotonic() > deadline:
            raise AssertionError(f"sharing {label} did not finish after the pick")
        machine.sleep(1)


def capture(name, source, cursor, label, shows="", pointer=None):
    ran = choose(label)
    extra = (f" --shows {shows}" if shows else "") + (f" --pointer {pointer[0]} {pointer[1]}" if pointer else "")
    machine.succeed(as_alice(f"{params['background']} {name} {params['screencast']} capture {source} {cursor} 10 /tmp/{name}{extra}"))
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


def check_unpainted(hidden, other, rect):
    print(machine.succeed(as_alice(f"{params['screencast']} unpainted /tmp/{hidden} /tmp/{other} {rect['x']} {rect['y']} {rect['width']} {rect['height']}")))


VIRTUAL_MODE = {"width": 1024, "height": 768, "refresh_mhz": 30000}
VIRTUAL_PIXELS = f"{VIRTUAL_MODE['width']}x{VIRTUAL_MODE['height']}"


def outputs():
    return {o["name"]: o for o in json.loads(machine.succeed(as_alice("lys-wmctl outputs")))}


def window_output(window_id):
    return next(w["output"] for w in windows() if w["id"] == window_id)


def capture_virtual(window_id):
    name = json.loads(machine.succeed(as_alice(f"lys-wmctl output create {VIRTUAL_PIXELS}@30")))["name"]
    listed = outputs()
    assert name in listed and listed[name]["mode"] == VIRTUAL_MODE, f"lys-wm lists {listed} after making {name}"
    real = listed[OUTPUT]["rect"]
    info = machine.succeed(as_alice(f"WAYLAND_DISPLAY={display} wayland-info"))
    for want in [f"name: {name}", f"logical_x: {real['x'] + real['width']}, logical_y: 0", f"logical_width: {VIRTUAL_MODE['width']}, logical_height: {VIRTUAL_MODE['height']}"]:
        assert want in info, f"wayland-info does not show {want!r} for {name}:\n{info}"
    machine.succeed(as_alice(f"lys-wmctl dispatch focuswindow {window_id}"))
    machine.succeed(as_alice(f"lys-wmctl dispatch movetoworkspace {listed[name]['workspace']}"))
    machine.wait_until_succeeds(as_alice("lys-wmctl windows") + f" | jq -e 'any(.[]; .id == {window_id} and .output == \"{name}\")'", timeout=30)
    capture("virtual", "monitor", "hidden", f"Monitor: {name}", shows="anything")
    log = machine.succeed("cat /tmp/virtual.log")
    assert f", {VIRTUAL_PIXELS}," in log, f"the stream of {name} is not {VIRTUAL_PIXELS}: {log}"
    print(machine.execute("journalctl -b --no-pager -t xdg-desktop-portal-wlr | grep -i -m5 dmabuf")[1])
    machine.succeed(as_alice(f"lys-wmctl output remove {name}"))
    assert list(outputs()) == [OUTPUT], f"outputs after removing {name}: {list(outputs())}"
    assert window_output(window_id) == OUTPUT, f"the window stayed on {window_output(window_id)} after {name} went"


CAST_SOURCE_CLASS = re.compile(r"^(Video/Source|Stream/Output/Video)$")


def media_class(obj):
    return ((obj.get("info") or {}).get("props") or {}).get("media.class", "")


XDPW_STARTED = re.compile(r"dbus: start: returning node (\d+)$")
XDPW_NEGOTIATED = "pipewire: Format negotiated:"
PAGE_STALL_S = int(re.search(r"const stallMs = (\d+);", open(params["page_source"]).read()).group(1)) / 1000
FRAMES_NEED_S = 1


def xdpw_negotiation(journal):
    node, started, negotiated = None, None, None
    for line in journal.splitlines():
        stamp, _, message = line.partition(" ")
        found = XDPW_STARTED.search(message)
        if found:
            node, started, negotiated = int(found.group(1)), float(stamp), None
        elif node is not None and negotiated is None and XDPW_NEGOTIATED in message:
            negotiated = float(stamp)
    return node, started, negotiated


def xdpw_stalled_side():
    node, started, negotiated = xdpw_negotiation(machine.succeed("journalctl -b --no-pager -o short-unix -t xdg-desktop-portal-wlr"))
    if node is None:
        return "the portal never started a stream"
    if negotiated is None:
        return f"PipeWire never gave the portal's stream {node} a format, so the portal had asked lys-wm for no frame"
    waited = negotiated - started
    left = PAGE_STALL_S - waited
    if left < FRAMES_NEED_S:
        return f"PipeWire gave the portal's stream {node} its format only {waited:.1f} s after it started, leaving {left:.1f} s of the page's {PAGE_STALL_S:.0f} s stall limit; the portal asks lys-wm for no frame before it has a format"
    return f"PipeWire gave the portal's stream {node} its format {waited:.1f} s after it started; no frame followed in the {left:.1f} s left, so lys-wm did not answer the portal's copy"


def stalled_side():
    if params["portal"] == "wlr":
        return xdpw_stalled_side()
    status, out = machine.execute(as_alice("pw-dump"))
    if status != 0:
        return f"pw-dump failed: {out.strip()}"
    objects = json.loads(out)
    nodes = [o for o in objects if o["type"] == "PipeWire:Interface:Node"]
    sources = [o["id"] for o in nodes if CAST_SOURCE_CLASS.match(media_class(o))]
    viewers = [o["id"] for o in nodes if media_class(o) == "Stream/Input/Video"]
    if not sources:
        return "the portal never started the stream: PipeWire has no video source node"
    links = [o["info"] for o in objects if o["type"] == "PipeWire:Interface:Link" and o["info"].get("output-node-id") in sources]
    if not any(link.get("state") == "active" for link in links):
        return f"PipeWire never negotiated: the portal's stream {sources} has links {[link.get('state') for link in links]} to viewers {viewers}"
    return f"PipeWire negotiated and linked the portal's stream {sources}; with no frame, lys-wm never answered the copy"


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
        machine.wait_until_succeeds(finished, timeout=params["seconds"] + CHOOSER_APPEARS_S)
    except Exception:
        dump(app)
        title = next((w["title"] for w in windows() if w["id"] == shown["id"]), "closed")
        print(f"{app} did not finish sharing {label}: {title}; {stalled_side()}")
        raise
    finally:
        machine.screenshot(f"{app}-{KEYS[label]}")
    title = next(w for w in windows() if w["id"] == shown["id"])["title"]
    print(f"{app} shared {label}: {title}; {stolen(started)} while it ran")
    fields = SHARE_FIELDS.match(title)
    if fields is None:
        dump(app)
        raise AssertionError(f"{app} could not share {label}: {title}; {stalled_side()}")
    size, _, seconds, _, gap, distinct, background = fields.groups()
    assert float(seconds) >= params["seconds"], f"{app} shared {label} for only {seconds} s"
    assert int(gap) <= params["max_gap_ms"], f"{app} stalled {gap} ms sharing {label}"
    assert int(distinct) >= 2, f"{app} shared {label} without live content"
    if label == MONITOR:
        assert size == OUTPUT_SIZE, f"{app} shared {OUTPUT} at {size}"
    else:
        target = window_titled(f"{ANIMATION}:")
        shared_size = [int(v) for v in size.split("x")]
        target_size = [target["rect"]["width"], target["rect"]["height"]]
        assert all(abs(s - t) <= ENCODER_ROUNDING for s, t in zip(shared_size, target_size)), f"{app} shared a {size} window, not the {target_size} {ANIMATION} window clicked: {[(w['id'], w['title'], w['rect']) for w in windows()]}"
        assert float(background) <= 0.01, f"{app} shared {label} with {background} of it the desktop"
    machine.succeed(f"kill {shown['pid']}")
    machine.wait_until_fails(f"kill -0 {shown['pid']}", timeout=60)


if params["portal"] != "wlr":
    machine.succeed(as_alice(f"{params['background']} select-sources {params['method_calls']} org.freedesktop.impl.portal.ScreenCast SelectSources"))
    machine.succeed(as_alice(f"{params['background']} pick-window {params['method_calls']} {PICKER_SERVICE} PickWindow"))
    machine.wait_until_succeeds("[ $(pgrep -c -u alice -x dbus-monitor) -eq 2 ]", timeout=30)
    machine.wait_until_succeeds(as_alice(f"busctl --user list --no-legend | grep -q {PICKER_SERVICE}"), timeout=CHOOSER_APPEARS_S)
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
if params["portal"] != "wlr":
    capture("monitor-metadata", "monitor", "metadata", MONITOR, pointer=POINTER_REST)
    check_unpainted("monitor-hidden", "monitor-metadata", around)
capture("window-hidden", "window", "hidden", WINDOW)
capture("window-embedded", "window", "embedded", WINDOW)
check_cursor("window-hidden", "window-embedded", {**around, "x": around["x"] - rect["x"], "y": around["y"] - rect["y"]}, POINTER_REST[0] - rect["x"], POINTER_REST[1] - rect["y"])

if params["portal"] == "wlr":
    capture_virtual(animation["id"])

machine.succeed(as_alice(f"mkdir -p /tmp/firefox && cp {params['firefox_prefs']} /tmp/firefox/user.js"))
for app, confirms in [("firefox", False), ("chromium", True), ("electron", False)]:
    for label in [MONITOR, WINDOW]:
        share(app, label, confirms)
