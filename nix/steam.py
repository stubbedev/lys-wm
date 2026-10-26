def steam():
    launch("steam")
    updater = wait_for("Steam's updater", lambda: window_titled("Steam", ""), 600)
    assert updater["floating"], f"Steam's fixed-size updater does not float: {updater}"
    machine.screenshot("steam-updater")
    print(f"Steam: the updater floats at logical {updater['rect']}")
    signin = wait_for("Steam's sign-in window", lambda: window_titled("Sign in to Steam", "steam"), 900)
    x = wait_for("the sign-in X window", lambda: x_window_titled("Sign in to Steam"))
    assert signin["floating"] and signin["focused"], f"the #27 rules did not float the sign-in window: {signin}"
    assert (x["width"], x["height"]) == (700, 440), f"the fixed-size sign-in window is {x['width']}x{x['height']} in X, want 700x440"
    want = to_x(signin["rect"])
    assert (x["x"], x["y"]) == (want["x"], want["y"]), f"the sign-in window is at {x_geometry(x)} in X for logical {signin['rect']}"
    wait_drawn(x, "the sign-in window")
    machine.screenshot("steam-signin")
    print(f"Steam: the sign-in window floats focused at {x_geometry(x)} in X for logical {signin['rect']}; signing in needs an account, so the test stops here")


try:
    assert_physical_root()
    steam()
except Exception:
    dump(["/tmp/steam.log"])
    raise
