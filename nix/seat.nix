{
  pkgs,
  lys-wm,
}: let
  tests = lys-wm.overrideAttrs (old: {
    pname = "lys-wm-seat-tests";
    doCheck = false;
    buildPhase = ''
      runHook preBuild
      mkdir -p "$out/bin"
      go test -c -o "$out/bin/drm.test" ./internal/backend/drm
      runHook postBuild
    '';
    installPhase = ''
      runHook preInstall
      runHook postInstall
    '';
    postFixup = "";
  });
  env = import ./test-env.nix pkgs;
in
  import ./vm-test.nix pkgs {
    name = "lys-wm-seat";
    timeout = 600;
    nodes.machine = {
      boot = {
        kernelModules = ["vkms" "udmabuf"];
        extraModprobeConfig = "options vkms enable_cursor=1 enable_overlay=1";
      };
      virtualisation = {
        graphics = true;
        cores = 2;
        memorySize = 2048;
        qemu.options = ["-vga none" "-device virtio-gpu-pci"];
      };
      environment = {
        variables = env // {GBM_BACKENDS_PATH = "${pkgs.mesa}/lib/gbm";};
        systemPackages = [pkgs.bpftrace];
      };
    };
    testScript = ''
      import re

      machine.wait_for_unit("multi-user.target")
      machine.succeed("test -c /dev/udmabuf")
      machine.wait_until_succeeds("readlink /sys/class/drm/card*/device | grep -q /vkms$")
      machine.succeed(
          "bpftrace -q -e 'fexit:drm_plane_get_damage_clips /retval != 0/ { printf(\"clip %d %d %d %d\\n\", retval->x1, retval->y1, retval->x2, retval->y2); }' > /tmp/damage.log 2>&1 &"
      )
      machine.wait_until_succeeds("pgrep -x bpftrace && sleep 2")
      machine.succeed("echo 0x10 > /sys/module/drm/parameters/debug", "dmesg -C")
      status, _ = machine.execute(
          "cd ${tests}/bin && LYS_WM_SEAT=1 HOME=/tmp TMPDIR=/tmp timeout 240 ./drm.test -test.count=1 -test.v -test.run 'OnASeat' > /tmp/seat.log 2>&1",
          timeout=300,
      )
      machine.succeed("echo 0 > /sys/module/drm/parameters/debug", "pkill -INT bpftrace || true")
      results = machine.succeed("cat /tmp/seat.log")
      print(results)
      assert status == 0, f"the seat tests failed with status {status}"
      skipped = re.findall(r"--- SKIP: (\S+)", results)
      assert not skipped, f"these seat tests did not run: {skipped}"
      kernel = machine.succeed("dmesg")
      requests = len(re.findall(r"Allocated atomic state", kernel))
      commits = len(re.findall(r"\] committing ", kernel))
      nonblocking = len(re.findall(r"\] committing \S+ nonblocking", kernel))
      print(f"drm.debug: {requests} atomic requests, {requests - commits} of them TEST_ONLY, {commits} commits of which {nonblocking} nonblocking")
      clips = [tuple(map(int, m)) for m in re.findall(r"clip (-?\d+) (-?\d+) (-?\d+) (-?\d+)", machine.succeed("cat /tmp/damage.log"))]
      print(f"damage clips the kernel holds for the planes: {clips}")
      assert clips.count((32, 32, 96, 96)) >= 6, f"the six small frames' clips did not reach the kernel: {clips}"
    '';
  }
