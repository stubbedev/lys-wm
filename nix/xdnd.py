def open_window(name):
    before = {w["id"] for w in windows()}
    launch(name)
    return wait_for(f"the {name} window", lambda: next((w for w in windows() if w["id"] not in before), None))


def middle(window):
    r = to_x(window["rect"])
    return r["x"] + r["width"] // 2, r["y"] + r["height"] // 2


def drop_zone(window):
    r = to_x(window["rect"])
    return r["x"] + 40, r["y"] + 16


def drag_between(source, target, steps=20):
    (x0, y0), (x1, y1) = middle(source), drop_zone(target)
    pointer_to(x0, y0)
    button(True)
    for i in range(1, steps + 1):
        pointer_to(x0 + (x1 - x0) * i // steps, y0 + (y1 - y0) * i // steps)
    time.sleep(1)
    button(False)


def dropped(log, path):
    wait_for(f"{path} in {log}", lambda: path in machine.succeed(f"cat {log}"), 60)


def drop_case(source_name, target_name, path):
    machine.succeed(f"echo {source_name} > {path} && chmod a+r {path}")
    target = open_window(target_name)
    machine.succeed(as_alice("lys-wmctl dispatch togglefloating"))
    wait_for(f"the {target_name} window tiled", lambda: not window_titled(target["title"], target["app_id"])["floating"])
    target = window_titled(target["title"], target["app_id"])
    source = open_window(source_name)
    time.sleep(2)
    try:
        drag_between(source, target)
        dropped(f"/tmp/{target_name}.log", path)
    except Exception:
        dump([f"/tmp/{source_name}.log", f"/tmp/{target_name}.log"])
        raise
    print(f"{source_name} dropped {path} into {target_name}")
    machine.execute("pkill -x dragon-drop")
    wait_for("the dragon windows closed", lambda: not windows(), 60)


drop_case("wayland-source", "x-target", "/tmp/from-wayland.txt")
drop_case("x-source", "wayland-target", "/tmp/from-x.txt")
