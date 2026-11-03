pkgs: let
  awaitSockets = real:
    pkgs.writeShellScript "qemu-awaiting-its-sockets" ''
      set -euo pipefail
      for arg in "$@"; do
        [[ $arg == socket,id=*,path=* ]] || continue
        socket=''${arg##*,path=}
        for ((left = 200; left > 0; left--)); do
          [[ -S $socket ]] && break
          ${pkgs.coreutils}/bin/sleep 0.05
        done
        if [[ ! -S $socket ]]; then
          echo "qemu: nothing created the socket $socket in 10 s" >&2
          exit 1
        fi
      done
      exec ${real} "$@"
    '';
  qemu = pkgs.symlinkJoin {
    name = "qemu-for-vm-tests-awaiting-virtiofsd";
    paths = [pkgs.qemu_test];
    postBuild = ''
      for real in ${pkgs.qemu_test}/bin/qemu-system-*; do
        name=$(basename "$real")
        rm "$out/bin/$name"
        ln -s ${awaitSockets "${pkgs.qemu_test}/bin/qemu-system-${pkgs.stdenv.hostPlatform.qemuArch}"} "$out/bin/$name"
      done
    '';
  };
in
  {timeout ? 900, ...} @ args:
    pkgs.testers.runNixOSTest (builtins.removeAttrs args ["timeout"]
      // {
        globalTimeout = timeout;
        qemu.package = qemu;
      })
