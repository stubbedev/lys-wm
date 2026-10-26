import datetime as dt
import json
import os
import re

PLANE_TYPES = {0: "overlay", 1: "primary", 2: "cursor"}
MOVES = 5


def device(card):
    return os.path.basename(machine.succeed(f"readlink -f /sys/class/drm/{card}/device").strip())


def plane_types():
    return {
        device(os.path.basename(path)): {p["id"]: PLANE_TYPES[p["properties"]["type"]["value"]] for p in card["planes"]}
        for path, card in json.loads(machine.succeed("drm_info -j")).items()
    }


def send(kind, x, y):
    machine.qmp_client.send("input-send-event", {"events": [
        {"type": kind, "data": {"axis": "x", "value": x}},
        {"type": kind, "data": {"axis": "y", "value": y}},
    ]})


def commits(name, move):
    start, end = f"lys-wm-cursor-{name}-start", f"lys-wm-cursor-{name}-end"
    machine.succeed("echo 0x10 > /sys/module/drm/parameters/debug", f"echo {start} > /dev/kmsg")
    for i in range(MOVES):
        move(i)
        machine.sleep(duration=dt.timedelta(milliseconds=500))
    machine.sleep(duration=dt.timedelta(seconds=1))
    machine.succeed(f"echo {end} > /dev/kmsg", "echo 0 > /sys/module/drm/parameters/debug")
    log = machine.succeed(f"dmesg | sed -n '/{start}/,/{end}/p'")
    states, committed = {}, []
    for dev, message in re.findall(r"(\S+): \[drm:[^\]]+\] (.*)", log):
        if m := re.match(r"Added \[PLANE:(\d+):[^\]]*\] \S+ state to (\S+)", message):
            states.setdefault((dev, m[2]), set()).add(types[dev][int(m[1])])
        elif m := re.match(r"Allocated atomic state (\S+)", message):
            states[dev, m[1]] = set()
        elif m := re.match(r"committing (\S+)", message):
            committed.append((dev, frozenset(states.get((dev, m[1]), set()))))
    print(f"{name}: {len(committed)} commits: {committed}")
    return committed


types = plane_types()
with_plane, without_plane = device("card*-Virtual-1/device"), device("card*-Virtual-2/device")
assert "cursor" in types[with_plane].values(), f"{with_plane} has no cursor plane: {types[with_plane]}"
assert "cursor" not in types[without_plane].values(), f"{without_plane} has a cursor plane: {types[without_plane]}"

send("abs", 0x2000, 0x2000)
machine.sleep(duration=dt.timedelta(seconds=2))
on_plane = commits("plane", lambda i: send("abs", 0x2400 + 0x600 * i, 0x2800 + 0x400 * i))
assert all(dev == with_plane and planes == {"cursor"} for dev, planes in on_plane), f"moving the pointer over Virtual-1 committed more than its cursor plane: {on_plane}"
assert len(on_plane) >= MOVES, f"{MOVES} moves over Virtual-1 made only {len(on_plane)} cursor commits"

send("rel", 0x4000, 0)
machine.sleep(duration=dt.timedelta(seconds=2))
in_frame = commits("frame", lambda i: send("rel", -20, 10))
assert all("primary" not in planes for dev, planes in in_frame if dev == with_plane), f"moving the pointer over Virtual-2 repainted Virtual-1: {in_frame}"
framed = [planes for dev, planes in in_frame if dev == without_plane and "primary" in planes]
assert len(framed) >= MOVES, f"{MOVES} moves over Virtual-2, which has no cursor plane, made only {len(framed)} frames"
