import json

RUNTIME = "/run/user/1000"


def as_alice(command):
    return params["as_alice"].format(command)


def wmctl(args):
    return as_alice(f"lys-wmctl {args}")


def windows():
    return sorted(json.loads(machine.succeed(wmctl("windows"))), key=lambda w: w["id"])


def layout(ws):
    return [(w["id"], w["app_id"], w["workspace"], w["output"], w["rect"], w["floating"], w["fullscreen"], w["focused"]) for w in ws]


def dump():
    print(machine.execute("journalctl -b --no-pager -t lys-wm | tail -80")[1])
    print(machine.execute("for f in /tmp/*.log; do echo \"== $f\"; tail -20 \"$f\"; done")[1])
    print(machine.execute(wmctl("windows"))[1])


def screen_hash():
    return machine.succeed(as_alice(f"WAYLAND_DISPLAY={display} grim -t ppm -o Virtual-1 -") + " | sha256sum").split()[0]


def steady_hash():
    seen = screen_hash()
    for _ in range(30):
        machine.sleep(1)
        now = screen_hash()
        if now == seen:
            return now
        seen = now
    raise Exception("the screen never settled")


def worker():
    return machine.succeed("pgrep -x -n lys-wm-next || pgrep -x -n lys-wm").strip()


def typed_contains(text):
    machine.wait_until_succeeds(f"grep -q {text} /tmp/typed", timeout=30)


def type_into_focused(text):
    machine.succeed(as_alice(f"WAYLAND_DISPLAY={display} wtype {text}"))
    machine.succeed(as_alice(f"WAYLAND_DISPLAY={display} wtype -k Return"))


def check_kept(before, why):
    after = windows()
    assert layout(after) == layout(before), f"{why}: layout {layout(after)}, was {layout(before)}"
    for w in before:
        machine.succeed(f"kill -0 {w['pid']}")


try:
    machine.succeed(wmctl(f"dispatch exec {params['mako']}"))
    machine.wait_until_succeeds("pgrep -u alice mako", timeout=60)
    machine.sleep(2)
    machine.succeed(wmctl(f"dispatch exec {params['notify']}"))
    machine.sleep(2)
    machine.succeed(as_alice(f"lys-wmctl dispatch exec {params['typed']}"))
    machine.wait_until_succeeds(wmctl("windows") + " | jq -e 'map(select(.app_id == \"typed\")) | length == 1'", timeout=60)
    machine.wait_until_succeeds("test -e /tmp/typed", timeout=30)
    machine.succeed(wmctl("dispatch workspace 2"))
    machine.succeed(as_alice(f"lys-wmctl dispatch exec {params['idle']}"))
    machine.wait_until_succeeds(wmctl("windows") + " | jq -e 'length == 2'", timeout=60)
    machine.succeed(wmctl("dispatch workspace 1"))
    machine.sleep(2)
    type_into_focused("beforeupgrade")
    typed_contains("beforeupgrade")

    before = windows()
    assert len(before) == 2, before
    old_worker = worker()
    holder = machine.succeed("pgrep -x -o lys-wm").strip()
    before_screen = steady_hash()

    status, output = machine.execute(wmctl(f"upgrade {params['bad']}") + " 2>&1")
    print(output)
    assert status != 0, f"a broken binary was taken: {output}"
    assert "breaks on purpose" in output and "keeps running" in output, output
    check_kept(before, "the failed upgrade")
    assert worker() == old_worker, "the worker changed on a failed upgrade"
    assert steady_hash() == before_screen, "the screen changed across a failed upgrade"
    type_into_focused("afterfailure")
    typed_contains("afterfailure")
    before_screen = steady_hash()

    status, output = machine.execute(wmctl("upgrade /run/current-system/sw/bin/true") + " 2>&1")
    assert status != 0 and ("does not answer" in output or "not a handover" in output), output
    check_kept(before, "the refused upgrade")

    machine.succeed("cp -L $(command -v lys-wm) /tmp/lys-wm-next && chmod a+rx /tmp/lys-wm-next")
    print(machine.succeed(wmctl("upgrade /tmp/lys-wm-next") + " 2>&1"))
    assert machine.succeed("pgrep -x -o lys-wm").strip() == holder, "the session's holder changed"
    new_worker = machine.succeed("pgrep -x lys-wm-next").strip()
    assert machine.succeed(f"readlink /proc/{new_worker}/exe").strip() == "/tmp/lys-wm-next"
    assert machine.execute(f"kill -0 {old_worker}")[0] != 0, "the old compositor is still running"
    check_kept(before, "the upgrade")
    machine.succeed(f"grep -qx enabled /sys/class/drm/card*-Virtual-1/enabled")
    after_screen = steady_hash()
    assert after_screen == before_screen, "the screen changed across the upgrade"
    type_into_focused("afterupgrade")
    typed_contains("afterupgrade")

    machine.send_key("meta_l-ret")
    machine.wait_until_succeeds(wmctl("windows") + " | jq -e 'length == 3'", timeout=60)
    machine.send_key("ctrl-alt-f2")
    machine.sleep(3)
    machine.send_key("ctrl-alt-f1")
    machine.sleep(3)
    machine.wait_until_succeeds(as_alice(f"WAYLAND_DISPLAY={display} grim -o Virtual-1 /tmp/after-vt.png"), timeout=30)
    machine.send_key("meta_l-ret")
    machine.wait_until_succeeds(wmctl("windows") + " | jq -e 'length == 4'", timeout=60)

    machine.succeed("cp -L /tmp/lys-wm-next /tmp/lys-wm-third && chmod a+rx /tmp/lys-wm-third")
    before = windows()
    print(machine.succeed(wmctl("upgrade /tmp/lys-wm-third") + " 2>&1"))
    machine.succeed("pgrep -x lys-wm-third")
    check_kept(before, "the second upgrade")

    machine.succeed(wmctl("dispatch workspace 3"))
    machine.succeed(as_alice(f"lys-wmctl dispatch exec {params['xterm']}"))
    machine.wait_until_succeeds(wmctl("windows") + " | jq -e 'map(select(.app_id | test(\"XTerm|xterm\"))) | length == 1'", timeout=90)
    status, output = machine.execute(wmctl("upgrade /tmp/lys-wm-next") + " 2>&1")
    print(output)
    assert status != 0 and "refused, nothing changed" in output and "drop-x11" in output, output
    machine.succeed("pgrep -u alice xterm")
    wayland_only = [w for w in windows() if "xterm" not in w["app_id"].lower()]
    print(machine.succeed(wmctl("upgrade --drop-x11 /tmp/lys-wm-next") + " 2>&1"))
    machine.wait_until_fails("pgrep -u alice xterm", timeout=30)
    kept = windows()
    assert [(w["id"], w["app_id"], w["workspace"], w["rect"]) for w in kept] == [(w["id"], w["app_id"], w["workspace"], w["rect"]) for w in wayland_only], f"dropping Xwayland: {kept}, was {wayland_only}"
    for w in wayland_only:
        machine.succeed(f"kill -0 {w['pid']}")
    machine.succeed(as_alice(f"lys-wmctl dispatch exec {params['xterm']}"))
    machine.wait_until_succeeds(wmctl("windows") + " | jq -e 'map(select(.app_id | test(\"XTerm|xterm\"))) | length == 1'", timeout=90)

    machine.succeed("printf 'xwayland:\\n  enabled: false\\n' > /tmp/headless.yaml && chmod a+r /tmp/headless.yaml")
    machine.succeed(as_alice(params["headless"]) + " > /dev/null 2>&1 &")
    machine.wait_until_succeeds("ls /tmp/headless/lys-wm/*.sock", timeout=30)
    head_socket = machine.succeed("ls /tmp/headless/lys-wm/*.sock").strip()
    head_display = machine.succeed("ls /tmp/headless | grep -m1 '^wayland-[0-9]*$'").strip()
    machine.succeed(as_alice(f"{params['headless_foot']} {head_display}") + " > /dev/null 2>&1 &")
    machine.wait_until_succeeds(f"{as_alice(f'lys-wmctl --socket {head_socket} windows')} | jq -e 'length == 1'", timeout=60)
    first = json.loads(machine.succeed(as_alice(f"lys-wmctl --socket {head_socket} windows")))
    print(machine.succeed(as_alice(f"lys-wmctl --socket {head_socket} upgrade /tmp/lys-wm-third") + " 2>&1"))
    second = json.loads(machine.succeed(as_alice(f"lys-wmctl --socket {head_socket} windows")))
    assert layout(first) == layout(second), f"headless: {second}, was {first}"
    machine.succeed(f"kill -0 {first[0]['pid']}")
except Exception:
    dump()
    raise
