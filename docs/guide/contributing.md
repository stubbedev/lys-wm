# Contributing

[`AGENTS.md`](https://github.com/stubbedev/lys-wm/blob/main/AGENTS.md)
describes how work is picked and landed; [Architecture](../architecture) the
design every change builds on. Work is tracked in the
[issues](https://github.com/stubbedev/lys-wm/issues), grouped by
[milestone](https://github.com/stubbedev/lys-wm/milestones).

## Development environment

```sh
nix develop    # or direnv: use flake
```

The dev shell (`nix/shell.nix`) provides the Go toolchain, gopls,
golangci-lint, govulncheck and just, with `CGO_ENABLED=0` and
`GOTOOLCHAIN=local`.

## Gates

```sh
just docker gates    # format, tidy, vet, lint, tests, build, govulncheck, race detector, pacing gate
nix build -L         # the package, with its tests
```

Tests run in a container (`just docker <recipe>`) or a NixOS VM, never
against the host's desktop session. `just --list` shows every recipe.

## Flake checks

```sh
nix build -L .#checks.x86_64-linux.module    # evaluate the NixOS module
nix build -L .#checks.x86_64-linux.headless  # headless suite in the build sandbox
nix build -L .#checks.x86_64-linux.vm        # the module in a VM on the drm backend (needs KVM)
```

The other `vm-*` checks cover several GPUs, the cursor plane, the lid, rtkit,
upgrades, Xwayland, everyday clients and screen sharing.
[`ci.yml`](https://github.com/stubbedev/lys-wm/blob/main/.github/workflows/ci.yml)
runs each as its own job. [Frame pacing](../frame-pacing) covers
`lys-wm-pace` and its gate.

## This site

The pages live in `docs/` and every push to `main` publishes them.
[Actions](../actions) is generated from `wm.Actions`: edit the action
summaries in `internal/wm/action.go` and run `go test ./internal/wm -update`.

```sh
cd docs
npm ci
npm run dev
```
