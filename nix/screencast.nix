{
  pkgs,
  module,
  seconds,
}: let
  firefoxPrefs = pkgs.writeText "user.js" ''
    user_pref("media.navigator.permission.disabled", true);
    user_pref("browser.aboutwelcome.enabled", false);
    user_pref("browser.shell.checkDefaultBrowser", false);
    user_pref("browser.startup.homepage_override.mstone", "ignore");
    user_pref("datareporting.policy.dataSubmissionEnabled", false);
    user_pref("toolkit.telemetry.reportingpolicy.firstRun", false);
  '';
  launch = import ./launch.nix pkgs;
  page = "file://${./screencast.html}?seconds=${toString seconds}";
  electronApp = pkgs.runCommand "electron-share" {} "mkdir $out && cp ${./electron-share}/* $out/ && cp ${./screencast.html} $out/screencast.html";
  python = pkgs.python3.withPackages (ps: [ps.dbus-python ps.pygobject3]);
  frames = pkgs.runCommandCC "pw-frames" {
    nativeBuildInputs = [pkgs.pkg-config];
    buildInputs = [pkgs.pipewire];
  } "mkdir -p $out/bin && $CC -std=gnu11 -O2 -Wall -Wextra -Werror -o $out/bin/pw-frames ${./pw-frames.c} $(pkg-config --cflags --libs libpipewire-0.3)";
  chooser = pkgs.writeShellScript "share-pick" ''
    set -euo pipefail
    ${pkgs.gnugrep}/bin/grep -m1 -F -- "$(${pkgs.coreutils}/bin/cat /tmp/share-pick)"
  '';
  launchers = builtins.mapAttrs launch {
    animation = "${pkgs.weston}/bin/weston-presentation-shm";
    firefox = "${pkgs.firefox}/bin/firefox --no-remote --profile /tmp/firefox '${page}'";
    chromium = "${pkgs.chromium}/bin/chromium --no-first-run --user-data-dir=/tmp/chromium '${page}'";
    electron = "${pkgs.electron}/bin/electron --ozone-platform=wayland ${electronApp} ${toString seconds}";
  };
in
  import ./vm.nix {
    inherit pkgs module;
    name = "lys-wm-screencast";
    devices = ["virtio-gpu-pci"];
    outputs = ["Virtual-1"];
    machineConfig = {config, ...}: {
      services.pipewire.enable = true;
      virtualisation.memorySize = pkgs.lib.mkForce 4096;
      environment.systemPackages = [python frames];
      xdg.portal.wlr.settings.screencast = {
        chooser_type = "dmenu";
        chooser_cmd = "${chooser}";
      };
      systemd.user.services.xdg-desktop-portal-wlr.serviceConfig.ExecStart = pkgs.lib.mkForce [
        ""
        "${pkgs.xdg-desktop-portal-wlr}/libexec/xdg-desktop-portal-wlr --loglevel=DEBUG --config=${(pkgs.formats.ini {}).generate "xdg-desktop-portal-wlr.ini" config.xdg.portal.wlr.settings}"
      ];
    };
    extraTest = run: let
      params = builtins.toJSON {
        as_alice = run "{}";
        screencast = "${python}/bin/python3 ${./screencast.py}";
        firefox_prefs = "${firefoxPrefs}";
        portal_log = "xdg-desktop-portal-wlr";
        chooser_ran = "wlroots: chooser called";
        max_gap_ms = 2000;
        inherit launchers seconds;
      };
    in ''
      import json
      params = json.loads(${builtins.toJSON params})
      exec(open("${./screencast-test.py}").read())
    '';
  }
