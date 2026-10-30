import json
import re
from datetime import timedelta

marker = "splash-handover: lit "
modeset = re.compile(r"Set \[MODE:|Link \[CONNECTOR:|mode changed|active changed|enable changed|connectors changed|requires full modeset")


def vkms_after_marker():
    log = machine.succeed("dmesg --notime")
    assert marker in log, "the splash helper left no mark in the kernel log"
    after = log[log.rindex(marker):].splitlines()
    print(after[0])
    return [line for line in after[1:] if " vkms: [drm:" in line]


def wait_for_outputs():
    machine.wait_until_succeeds(f"{lys_wmctl_outputs} | jq -e 'length == 2'", timeout=timedelta(seconds=120))
    return {o["name"]: o for o in json.loads(machine.succeed(lys_wmctl_outputs))}


def background_only(output):
    display = machine.succeed("ls /run/user/1000 | grep -m1 '^wayland-[0-9]*$'").strip()
    shot = f"WAYLAND_DISPLAY={display} grim -t ppm -o {output} -"
    pixels = machine.succeed(f"su alice -c 'XDG_RUNTIME_DIR=/run/user/1000 {shot}' | tail -n +4 | od -An -v -tx1 -w3 | sort -u").split("\n")
    pixels = [p.strip() for p in pixels if p.strip()]
    assert pixels == ["1e 1e 2e"], f"{output} shows {pixels[:4]}"


vkms = vkms_after_marker()
print("\n".join(vkms[:80]))
commits = [line for line in vkms if "] committing " in line]
assert commits, "lys-wm committed nothing on vkms"
assert commits[0].endswith(" nonblocking"), f"something but lys-wm committed on vkms first: {commits[0]}"
modesets = [line for line in vkms if modeset.search(line)]
assert not modesets, f"the hand-over modeset vkms: {modesets[:8]}"

machine.succeed("systemctl stop greetd.service")
machine.wait_until_fails("pgrep -x lys-wm", timeout=timedelta(seconds=60))
machine.succeed("systemd-run --unit=splash-handover-other --service-type=notify splash-handover other")
machine.succeed("systemctl start greetd.service")
outputs = wait_for_outputs()
mode = outputs["Virtual-2"]["mode"]
assert (mode["width"], mode["height"]) == (1024, 768), f"Virtual-2 came up in {mode}"
vkms = vkms_after_marker()
modesets = [line for line in vkms if modeset.search(line)]
print("\n".join(modesets[:8]))
assert modesets, "lys-wm took over a different mode without a modeset"
background_only("Virtual-2")
