{inputs, ...}: {
  perSystem = {
    inputs',
    pkgs,
    self',
    ...
  }: {
    devShells.default = pkgs.mkShell {
      inputsFrom = [self'.packages.lys-wm];

      packages = with pkgs;
        [
          gopls
          golangci-lint
          delve
          govulncheck
          git
          gh
        ]
        ++ ["${inputs.lys-ci}"]
        ++ import ./test-programs.nix {
          inherit pkgs;
          inherit (inputs'.lys-ui.packages) lys-ui;
        };

      env =
        import ./test-env.nix pkgs
        // {
          CGO_ENABLED = "0";
          GOTOOLCHAIN = "local";
        };
    };

    formatter = pkgs.alejandra;
  };
}
