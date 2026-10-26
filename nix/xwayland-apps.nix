{
  pkgs,
  module,
  app,
}: let
  launch = import ./launch.nix pkgs;
  idea = pkgs.jetbrains.idea;
  ideaDir = "IntelliJIdea${pkgs.lib.versions.majorMinor idea.version}";
  project = pkgs.writeTextDir "src/Main.java" ''
    public class Main {
        private int counter;

        void increment() {
            counter++;
        }

        void run() {
            int ready = 0;
        }
    }
  '';
  apps = {
    idea = {
      script = ./idea.py;
      params = {
        inherit project;
        vmoptions = "/home/alice/.config/JetBrains/${ideaDir}/idea64.vmoptions";
        launchers = {
          idea = launch "idea" "${pkgs.lib.getExe idea} /home/alice/demo";
          main-java = launch "main-java" "${pkgs.lib.getExe idea} /home/alice/demo/src/Main.java";
        };
      };
    };
    steam = {
      script = ./steam.py;
      params.launchers.steam = launch "steam" "steam";
    };
  };
in
  import ./vm.nix {
    inherit pkgs module;
    name = "lys-wm-xwayland-${app}";
    devices = ["virtio-gpu-pci"];
    outputs = ["Virtual-1"];
    lysWMConfig = ''
      outputs:
        - {match: Virtual-1, scale: 1.5}
      binds:
        - {chord: SUPER + mouse:272, action: movewindow}
      rules:
        - {app_id: ^steam$, float: true}
        - {app_id: ^steam$, title: ^Steam$, tile: true}
        - {app_id: ^steam$, title: ^Friends List$, size: 400x900}
    '';
    machineConfig = {
      virtualisation = {
        memorySize = pkgs.lib.mkForce 6144;
        cores = pkgs.lib.mkForce 4;
        diskSize = 8192;
      };
      programs.steam.enable = app == "steam";
      fonts.packages = [pkgs.dejavu_fonts];
      environment.systemPackages = [pkgs.xorg.xwininfo pkgs.xdotool];
    };
    extraTest = run: let
      params = builtins.toJSON ({
          as_alice = run "{}";
          xwindows = import ./xwindows.nix pkgs;
        }
        // apps.${app}.params);
    in ''
      import json
      params = json.loads(${builtins.toJSON params})
      exec(open("${./xwayland-lib.py}").read())
      exec(open("${apps.${app}.script}").read())
    '';
  }
