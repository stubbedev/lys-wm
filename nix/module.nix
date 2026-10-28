{self, ...}: let
  sessionTarget = "lys-wm-session.target";

  options = {
    lib,
    pkgs,
    description,
  }: {
    enable = lib.mkEnableOption description;
    package = lib.mkOption {
      type = lib.types.package;
      default = self.packages.${pkgs.stdenv.hostPlatform.system}.lys-wm;
      defaultText = lib.literalExpression "lys-wm.packages.\${pkgs.stdenv.hostPlatform.system}.lys-wm";
      description = "The lys-wm package to run.";
    };
  };
in {
  flake.nixosModules.default = {
    config,
    lib,
    pkgs,
    modulesPath,
    ...
  }: let
    cfg = config.programs.lys-wm;
  in {
    options.programs.lys-wm = options {
      inherit lib pkgs;
      description = "lys-wm, a tiling Wayland compositor, as a display manager session";
    };

    config = lib.mkIf cfg.enable (lib.mkMerge [
      {
        environment.systemPackages = [cfg.package];
        systemd.packages = [cfg.package];
        services.displayManager.sessionPackages = [cfg.package];
        xdg.portal = {
          enable = true;
          configPackages = lib.mkDefault [cfg.package];
        };
      }
      (import (modulesPath + "/programs/wayland/wayland-session.nix") {
        inherit lib pkgs;
        enableXWayland = false;
      })
    ]);
  };

  flake.homeManagerModules.default = {
    config,
    lib,
    pkgs,
    ...
  }: let
    cfg = config.programs.lys-wm;
  in {
    options.programs.lys-wm = options {
      inherit lib pkgs;
      description = "lys-wm, a tiling Wayland compositor, and its systemd user session target";
    };

    config = lib.mkIf cfg.enable {
      home.packages = [cfg.package];
      xdg.configFile."systemd/user/${sessionTarget}".source = "${cfg.package}/share/systemd/user/${sessionTarget}";
    };
  };
}
