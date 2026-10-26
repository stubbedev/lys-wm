IDEA = "jetbrains-idea"


def idea_window(title, timeout=120):
    def found():
        w = window_titled(title, IDEA)
        x = w and x_window_titled(title)
        return (w, x) if x else None

    return wait_for(f"the IDEA window {title!r}", found, timeout)


def gone(title):
    wait_for(f"{title!r} to close", lambda: window_titled(title, IDEA) is None, 60)


def escape(title):
    def closed():
        if window_titled(title, IDEA) is None:
            return True
        machine.send_key("esc")
        return False

    wait_for(f"Escape to close {title!r}", closed, 60)


def focused(window):
    return next(w for w in windows() if w["id"] == window["id"])["focused"]


def new_x_window(before, what, timeout=30):
    return wait_for(what, lambda: next((w for w in x_windows() if w["id"] not in before and w["width"] > 1), None), timeout)


def inside(inner, outer):
    return outer["x"] <= inner["x"] and outer["y"] <= inner["y"] and inner["x"] + inner["width"] <= outer["x"] + outer["width"] and inner["y"] + inner["height"] <= outer["y"] + outer["height"]


def first_run():
    machine.succeed(f"mkdir -p /home/alice/demo && cp -r {params['project']}/. /home/alice/demo && chmod -R u+w /home/alice/demo && chown -R alice:users /home/alice/demo")
    machine.succeed(as_alice(f"install -D /dev/null {params['vmoptions']}"))
    machine.succeed(f"echo -Dawt.toolkit.name=XToolkit > {params['vmoptions']}")
    launch("idea")
    agreement, x = idea_window("IntelliJ IDEA User Agreement", 600)
    assert_unscaled(agreement, x)
    wait_drawn(x, "the user agreement")
    machine.screenshot("idea-agreement")
    click(x["x"] + 41, x["y"] + x["height"] - 69)
    click(x["x"] + x["width"] - 62, x["y"] + x["height"] - 30)
    sharing, x = idea_window("Data Sharing")
    assert_unscaled(sharing, x)
    click(x["x"] + x["width"] - 278, x["y"] + x["height"] - 30)
    trust, x = idea_window(" ", 300)
    assert_unscaled(trust, x)
    machine.screenshot("idea-trust")
    click(x["x"] + x["width"] - 365, x["y"] + x["height"] - 36)
    main, x = idea_window("demo", 600)
    assert_unscaled(main, x)
    print(f"IDEA: past the agreement, data sharing and trust dialogs by clicking; the project window is at {x_geometry(x)} in X for logical {main['rect']}")
    tour, tour_x = idea_window(" ", 300)
    minimum = (386, 482)
    assert tour["floating"] and tour["focused"], f"the theme tour is not a focused floating dialog: {tour}"
    assert (tour_x["width"], tour_x["height"]) == minimum, f"the theme tour is {tour_x['width']}x{tour_x['height']} in X, want its minimum {minimum}"
    assert_unscaled(tour, tour_x)
    assert inside(tour_x, x), f"the theme tour at {x_geometry(tour_x)} is not over the project window {x_geometry(x)}"
    machine.screenshot("idea-tour")
    escape(" ")
    print(f"IDEA: the theme tour, mapped at 1x1 with a {minimum[0]}x{minimum[1]} minimum, floats at {x_geometry(tour_x)} in X for logical {tour['rect']}")
    return main, x


def completion():
    launch("main-java")
    main, frame = idea_window("demo – Main.java", 120)
    before = {w["id"] for w in windows()}
    machine.send_key("ctrl-g")
    go_to = wait_for("the Go to Line dialog", lambda: next((w for w in windows() if w["id"] not in before), None))
    assert go_to["floating"] and go_to["focused"], f"Go to Line is not a focused floating dialog: {go_to}"
    machine.send_chars("9\n")
    wait_for("Go to Line to close", lambda: all(w["id"] != go_to["id"] for w in windows()), 60)
    machine.send_key("end")
    machine.send_key("ret")
    popup = None
    for _ in range(60):
        machine.send_key("shift-home")
        machine.send_chars("incr")
        before = {w["id"] for w in x_windows()}
        machine.send_key("ctrl-spc")
        try:
            popup = new_x_window(before, "the completion popup", 20)
        except AssertionError:
            machine.send_key("esc")
            machine.sleep(10)
            continue
        assert inside(popup, frame), f"the completion popup at {x_geometry(popup)} is outside the IDEA window {x_geometry(frame)}"
        machine.sleep(3)
        machine.screenshot("idea-completion")
        machine.send_key("ret")
        machine.send_key("ctrl-s")
        if machine.execute("timeout 15 sh -c 'until grep -q increment\\(\\)\\; /home/alice/demo/src/Main.java; do sleep 1; done'")[0] == 0:
            break
        popup = None
    assert popup, "no completion popup completed incr while IDEA analyzed the project"
    assert focused(main), "the IDEA window lost the focus to its completion popup"
    print(f"IDEA: Go to Line floated focused, the completion popup opened at {x_geometry(popup)} inside the editor, and its choice reached Main.java")
    return main, frame


def settings(main):
    machine.send_key("ctrl-alt-s")
    dialog, x = idea_window("Settings – demo")
    assert dialog["floating"] and dialog["focused"], f"Settings is not a focused floating dialog: {dialog}"
    assert_unscaled(dialog, x)
    drag(x["x"] + x["width"] // 2, x["y"] + 10, 150, 60)
    def moved_away():
        m = x_window_titled("Settings – demo")
        return m if m and (m["x"], m["y"]) != (x["x"], x["y"]) else None

    moved = wait_for("Settings to move", moved_away)
    assert abs(moved["x"] - x["x"] - 150) <= 1 and abs(moved["y"] - x["y"] - 60) <= 1, f"Settings moved from {x_geometry(x)} to {x_geometry(moved)}, want 150,60 further"
    assert_unscaled(window_titled("Settings – demo", IDEA), moved)
    before = {w["id"] for w in x_windows()}
    combo = (moved["x"] + 394, moved["y"] + 67)
    click(*combo)
    popup = new_x_window(before, "the theme list")
    assert abs(popup["y"] - combo[1]) < 40 and moved["x"] < popup["x"] <= combo[0], f"the theme list at {x_geometry(popup)} is not under its combo box at {combo}"
    machine.screenshot("idea-settings-moved")
    machine.send_key("esc")
    print(f"IDEA: Settings floated focused, moved by 150,60 to {x_geometry(moved)}, and its theme list opened under the combo box at {x_geometry(popup)}")
    machine.send_key("ctrl-shift-f")
    nested, nested_x = idea_window(" ")
    assert nested["floating"] and nested["focused"], f"Find in Files over Settings is not a focused floating window: {nested}"
    assert_unscaled(nested, nested_x)
    wait_drawn(nested_x, "Find in Files over Settings")
    machine.screenshot("idea-find-over-settings")
    escape(" ")
    print(f"IDEA: Find in Files over Settings floated at {x_geometry(nested_x)} in X for logical {nested['rect']}")
    click(moved["x"] + moved["width"] - 136, moved["y"] + moved["height"] - 26)
    gone("Settings – demo")
    wait_for("the project window focused again", lambda: focused(main), 30)


def find_in_files(main, frame):
    machine.send_key("ctrl-shift-f")
    popup, x = idea_window(" ")
    assert popup["floating"] and popup["focused"], f"Find in Files is not a focused floating window: {popup}"
    assert_unscaled(popup, x)
    assert inside(x, frame), f"Find in Files at {x_geometry(x)} is outside the IDEA window {x_geometry(frame)}"
    machine.send_chars("counter")
    machine.sleep(3)
    machine.screenshot("idea-find")
    escape(" ")
    wait_for("the project window focused again", lambda: focused(main), 30)
    print(f"IDEA: Find in Files floated focused at {x_geometry(x)} for logical {popup['rect']}")


try:
    assert_physical_root()
    first_run()
    main, frame = completion()
    settings(main)
    find_in_files(main, frame)
except Exception:
    dump(["/tmp/idea.log"])
    raise
