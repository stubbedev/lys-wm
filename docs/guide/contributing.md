# Contributing

Read [`AGENTS.md`](https://github.com/stubbedev/lys-wm/blob/main/AGENTS.md) for
how work is picked and landed, and [Architecture](../architecture) for the
decisions every change builds on. The plan lives in the
[issues](https://github.com/stubbedev/lys-wm/issues), grouped by
[milestone](https://github.com/stubbedev/lys-wm/milestones), each with its exit
criteria in its description.

## Development environment

```sh
nix develop    # Go, gopls, golangci-lint, govulncheck, just (or direnv: use flake)
```

The flake's dev shell (`nix/shell.nix`) is the whole development environment: the
package's Go toolchain, `CGO_ENABLED=0` and `GOTOOLCHAIN=local`.

## Gates

```sh
just docker gates    # format, tidy, vet, lint, tests, build, govulncheck, headless harness, race detector
nix build -L         # the package, with its tests
```

Every test runs in a container (`just docker <recipe>`) or a NixOS VM, never
against the desktop session on your host. `just --list` shows every recipe.

## Checks

The flake's `checks` hold the rest, each built by name:

```sh
nix build -L .#checks.x86_64-linux.module    # evaluates the NixOS module
nix build -L .#checks.x86_64-linux.headless  # the headless suite in the build sandbox
nix build -L .#checks.x86_64-linux.vm        # the module in a VM, on the drm backend
```

`checks.vm` boots the NixOS module in a VM with a virtio-gpu card, where greetd
starts `lys-wm --systemd` on the native DRM backend; it needs KVM. The other VMs
cover several GPUs, the cursor plane, Xwayland, everyday clients and screen
sharing, and [`ci.yml`](https://github.com/stubbedev/lys-wm/blob/main/.github/workflows/ci.yml)
runs them as their own jobs.

## This site

The pages are in `docs/`, and every push to `main` publishes them.
[Actions](../actions) is generated from `wm.Actions`: run
`go test ./internal/wm -update` instead of editing it.

```sh
cd docs
npm install
npm run dev
```
