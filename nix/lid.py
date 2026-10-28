import datetime as dt
import json

SETTLE = dt.timedelta(seconds=5)
LOGIN1 = "org.freedesktop.login1 /org/freedesktop/login1 org.freedesktop.login1.Manager"
SUSPENDS = f"journalctl -b -q -o cat -t {suspend_tag} | wc -l"


def suspends():
    return int(machine.succeed(SUSPENDS))


def set_lid(state):
    machine.succeed(f"echo {state} > {lid_fifo}")
    closed = {"open": "false", "closed": "true"}[state]
    machine.wait_until_succeeds(f"busctl get-property {LOGIN1} LidClosed | grep -qx 'b {closed}'", timeout=30)


def set_ac(state):
    machine.succeed(f"echo {state} > /sys/module/test_power/parameters/ac_online")
    online = {"on": "1", "off": "0"}[state]
    machine.wait_until_succeeds(f"grep -qx {online} /sys/class/power_supply/test_ac/online", timeout=30)


def unplug_external():
    machine.succeed(f"echo 1 > /sys/class/drm/{external_card}/device/remove")
    machine.wait_until_succeeds(f"{lys_wmctl_outputs} | jq -e 'length == 0'", timeout=60)


def stays_awake(why, change):
    before = suspends()
    change()
    machine.sleep(duration=SETTLE)
    after = suspends()
    assert after == before, f"{why}: suspended {after - before} times"


def suspends_once(why, change):
    before = suspends()
    change()
    try:
        machine.wait_until_succeeds(f"test $({SUSPENDS}) -gt {before}", timeout=30)
    except Exception:
        print(machine.execute("journalctl -b --no-pager -t lys-wm -u systemd-logind")[1])
        raise
    machine.sleep(duration=SETTLE)
    after = suspends()
    assert after == before + 1, f"{why}: suspended {after - before} times, want once"


machine.wait_until_succeeds(f"test -p {lid_fifo}", timeout=30)
inhibitors = json.loads(machine.succeed(f"busctl --json=short call {LOGIN1} ListInhibitors"))["data"][0]
assert any(what == "handle-lid-switch" and who == "lys-wm" and mode == "block" for what, who, _, mode, _, _ in inhibitors), f"lys-wm holds no blocking handle-lid-switch inhibitor: {inhibitors}"
machine.succeed("grep -qx Mains /sys/class/power_supply/test_ac/type", "grep -qx 1 /sys/class/power_supply/test_ac/online")
machine.succeed("grep -qx USB /sys/class/power_supply/test_usb/type", "grep -qx 0 /sys/class/power_supply/test_usb/online")
assert suspends() == 0, "suspended before the lid closed"

stays_awake("closing the lid docked on AC", lambda: set_lid("closed"))
stays_awake("unplugging AC with the lid closed while docked", lambda: set_ac("off"))
suspends_once("undocking on battery with the lid closed", unplug_external)
stays_awake("plugging in AC with the lid closed, undocked", lambda: set_ac("on"))
stays_awake("opening the lid undocked on AC", lambda: set_lid("open"))
stays_awake("closing the lid undocked on AC", lambda: set_lid("closed"))
suspends_once("unplugging AC with the lid closed, undocked", lambda: set_ac("off"))
machine.succeed("pgrep -x lys-wm")
