{
  description = "tilde - a pure-Go tiling Wayland compositor";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = {
    self,
    nixpkgs,
    flake-utils,
  }:
    flake-utils.lib.eachDefaultSystem (
      system: let
        pkgs = nixpkgs.legacyPackages.${system};
        # Nix has no version to read from the source tree, so builds from a
        # checkout are stamped with the commit date and hash.
        rev = self.shortRev or self.dirtyShortRev or "unknown";
        version = "0-unstable-${self.lastModifiedDate or "0"}";
      in {
        packages = rec {
          tilde = pkgs.callPackage ./package.nix {inherit rev version;};
          default = tilde;
        };

        apps = rec {
          tilde = flake-utils.lib.mkApp {drv = self.packages.${system}.tilde;};
          default = tilde;
        };

        devShells.default = pkgs.mkShell {
          packages = with pkgs; [
            # Go toolchain. Pinned to the version go.mod declares: the
            # default `go` attribute lags behind, and with GOTOOLCHAIN=auto
            # it would silently download its own toolchain instead of
            # failing. gopls and golangci-lint are built with the same
            # nixpkgs Go, so they parse what the compiler accepts.
            go_1_27

            # Development tools
            gopls # Go language server
            golangci-lint # Linter behind `just lint`, config in .golangci.yml
            delve # Go debugger
            just # Task runner

            # Development workflow
            git # Version control
            gh # GitHub CLI
            govulncheck # Vulnerability gate behind `just vuln`
          ];

          shellHook = ''
            # Keep accidental cgo out, and keep the compiler the flake
            # provides: a module asking for a newer Go fails instead of
            # downloading one at first use.
            export CGO_ENABLED=0 GOTOOLCHAIN=local
          '';
        };

        formatter = pkgs.alejandra;
      }
    );
}
