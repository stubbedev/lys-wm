RED, BLUE = b"\xff\x00\x00", b"\x00\x00\xff"
SIZE = 64


def output_pixels(name):
    return scanout(name, params["heads"][name])


def checker_pixels(pixels):
    return [i for i in range(len(pixels) // 3) if pixels[3 * i : 3 * i + 3] in (RED, BLUE)]


def checkerboard_on(name):
    width, height, pixels = output_pixels(name)
    hits = [(i % width, i // width) for i in checker_pixels(pixels)]
    assert hits, f"{name} shows no checkerboard pixel"
    left, top = min(x for x, _ in hits), min(y for _, y in hits)
    right, bottom = max(x for x, _ in hits) + 1, max(y for _, y in hits) + 1
    assert (right - left, bottom - top) == (SIZE, SIZE), f"{name}: the {SIZE}x{SIZE} checkerboard covers {right - left}x{bottom - top} pixels"
    for y in range(top, bottom):
        for x in range(left, right):
            want = RED if (x - left + y - top) % 2 == 0 else BLUE
            got = pixels[3 * (y * width + x) : 3 * (y * width + x) + 3]
            assert got == want, f"{name}: pixel {x},{y} is {got.hex()}, want {want.hex()}: the X buffer was resampled"
    return left, top


def open_checker(output):
    before = {w["id"] for w in windows()}
    launch(output)
    window = wait_for(f"a checker on {output}", lambda: next((w for w in windows() if w["id"] not in before), None))
    assert window["output"] == output, f"the checker opened on {window['output']}, want {output}"
    x_window = wait_for(f"the X window of the checker on {output}", lambda: next((w for w in x_windows() if w["class"] == window["app_id"]), None))
    wait_for(f"the checker drawn on {output}", lambda: checker_pixels(output_pixels(output)[2]))
    assert_unscaled(window, x_window)
    return window, x_window


try:
    outs = {o["name"]: o for o in outputs()}
    largest = max(o["scale"] for o in outs.values())
    assert_physical_root()
    for o in outs.values():
        x, _ = x_origin(o, largest)
        print(f"{o['name']}: scale {o['scale']}, logical {o['rect']}, X origin {x}")

    hidpi, hidpi_x = open_checker("Virtual-1")
    machine.succeed(as_alice("lys-wmctl dispatch focus right"))
    plain, plain_x = open_checker("Virtual-2")
    assert plain_x["x"] == outs["Virtual-1"]["mode"]["width"] + plain["rect"]["x"] - outs["Virtual-2"]["rect"]["x"], f"the plain output's window is at X {plain_x['x']}"
    print(f"HiDPI checker: logical {hidpi['rect']}, X {x_geometry(hidpi_x)}")
    print(f"plain checker: logical {plain['rect']}, X {x_geometry(plain_x)}")

    for name in ("Virtual-1", "Virtual-2"):
        print(f"{name}: the checkerboard lands pixel for pixel at {checkerboard_on(name)}")

    start = (hidpi["rect"]["x"] + hidpi["rect"]["width"] // 3, hidpi["rect"]["y"] + hidpi["rect"]["height"] // 3)
    for name, window in (("Virtual-1", hidpi), ("Virtual-2", plain)):
        r, o = window["rect"], outs[name]
        lx, ly = r["x"] + r["width"] // 3, r["y"] + r["height"] // 3
        pointer_to(device(start[0], largest), device(start[1], largest))
        pointer_by(lx - start[0], ly - start[1])
        ox, oy = x_origin(o, largest)
        want = (ox + device(lx - o["rect"]["x"], o["scale"]), oy + device(ly - o["rect"]["y"], o["scale"]))
        seen = []

        def arrived():
            seen.append(x_pointer())
            p = seen[-1]
            return p if abs(p[0] - want[0]) <= 1 and abs(p[1] - want[1]) <= 1 else None

        try:
            got = wait_for(f"the X pointer over the {name} checker", arrived, 30)
        except AssertionError:
            raise AssertionError(f"the pointer at logical {lx},{ly} put the X pointer at {seen[-1]}, want {want}") from None
        print(f"{name}: the pointer at logical {lx},{ly} is at {got} in X, want {want}")
except Exception:
    dump(["/tmp/checker-hidpi.log", "/tmp/checker-plain.log"])
    raise
