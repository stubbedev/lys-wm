{
  pkgs,
  module,
  lysShell,
  portal,
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
  background = pkgs.writeShellScript "background" ''
    set -euo pipefail
    name=$1
    shift
    ${pkgs.util-linux}/bin/setsid -f ${pkgs.bash}/bin/bash -c '"$@" > "/tmp/$0.log" 2>&1; echo $? > "/tmp/$0.status"' "$name" "$@" < /dev/null > /dev/null 2>&1
  '';
  selectSources = pkgs.writeShellScript "select-sources" ''
    exec ${pkgs.dbus}/bin/dbus-monitor --session "type='method_call',interface='org.freedesktop.impl.portal.ScreenCast',member='SelectSources'"
  '';
  portals = {
    wlr = {
      name = "lys-wm-screencast";
      log = "xdg-desktop-portal-wlr";
      chooserRan = "wlroots: chooser called";
      backgroundOnly = true;
      machineConfig = {config, ...}: let
        chooser = pkgs.writeShellScript "share-pick" ''
          set -euo pipefail
          ${pkgs.gnugrep}/bin/grep -m1 -F -- "$(${pkgs.coreutils}/bin/cat /tmp/share-pick)"
        '';
      in {
        xdg.portal.wlr.settings.screencast = {
          chooser_type = "dmenu";
          chooser_cmd = "${chooser}";
        };
        systemd.user.services.xdg-desktop-portal-wlr.serviceConfig.ExecStart = pkgs.lib.mkForce [
          ""
          "${pkgs.xdg-desktop-portal-wlr}/libexec/xdg-desktop-portal-wlr --loglevel=DEBUG --config=${(pkgs.formats.ini {}).generate "xdg-desktop-portal-wlr.ini" config.xdg.portal.wlr.settings}"
        ];
      };
    };
    lys-shell = {
      name = "lys-wm-screencast-lys-shell";
      log = "lys-shell";
      chooserRan = null;
      backgroundOnly = false;
      machineConfig = let
        wallpaper = pkgs.runCommand "desktop.png" {nativeBuildInputs = [pkgs.imagemagick];} "magick -size 64x64 xc:'#1e1e2e' png24:$out";
        shellConfig = (pkgs.formats.yaml {}).generate "config.yaml" {
          wallpaper = {
            inherit wallpaper;
            fit-mode = "fill";
          };
          share-picker.default-page = "windows";
        };
      in {
        imports = [lysShell];
        programs.lys-shell = {
          enable = true;
          systemd.enable = true;
        };
        systemd.tmpfiles.rules = [
          "d /home/alice/.config 0755 alice users -"
          "d /home/alice/.config/lys-shell 0755 alice users -"
          "L+ /home/alice/.config/lys-shell/config.yaml - - - - ${shellConfig}"
        ];
        fonts.packages = [pkgs.inter];
      };
    };
  };
  chosen = portals.${portal};
  launchers = builtins.mapAttrs launch {
    animation = "${pkgs.weston}/bin/weston-presentation-shm";
    firefox = "${pkgs.firefox}/bin/firefox --no-remote --profile /tmp/firefox '${page}'";
    chromium = "${pkgs.chromium}/bin/chromium --no-first-run --user-data-dir=/tmp/chromium '${page}'";
    electron = "${pkgs.electron}/bin/electron --ozone-platform=wayland ${electronApp} ${toString seconds}";
  };
in
  import ./vm.nix {
    inherit pkgs module;
    inherit (chosen) name;
    devices = ["virtio-gpu-pci"];
    outputs = ["Virtual-1"];
    backgroundOnly = chosen.backgroundOnly;
    lysWMConfig = ''
      rules:
        - {title: ^presentation-shm:, tile: true}
    '';
    machineConfig = {
      imports = [chosen.machineConfig];
      services.pipewire.enable = true;
      virtualisation.memorySize = pkgs.lib.mkForce 4096;
      environment.systemPackages = [python frames];
    };
    extraTest = run: let
      params = builtins.toJSON {
        as_alice = run "{}";
        screencast = "${python}/bin/python3 ${./screencast.py}";
        firefox_prefs = "${firefoxPrefs}";
        portal_log = chosen.log;
        chooser_ran = chosen.chooserRan;
        max_gap_ms = 2000;
        background = "${background}";
        select_sources = "${selectSources}";
        inherit launchers portal seconds;
      };
    in ''
      import json
      params = json.loads(${builtins.toJSON params})
      exec(open("${./screencast-test.py}").read())
    '';
  }
