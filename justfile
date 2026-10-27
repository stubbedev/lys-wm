# lys-wm dev tasks.

# List all recipes with their descriptions.
default:
    @just --list

# Every gate a commit must pass: formatting, a tidy go.mod, the release
# gates, the vulnerability scan, the headless harness and the race detector.
# CI runs this in the dev shell.
gates: fmt-check tidy-check check vuln headless race

# The packages whose tests need dev-shell programs: the compositor-in-the-loop
# harness, which boots lys-wm on the headless backend and drives real clients
# against it, the session bus and GPU tests, and the end-to-end session in
# cmd/lys-wm.
headless_packages := "./internal/harness/... ./internal/backend/nested/... ./internal/systemd/... ./internal/session/... ./internal/drm/... ./internal/syncobj/... ./internal/gpu/... ./internal/render/gles/... ./internal/xwayland/... ./cmd/lys-wm/..."

# Run the tests that need dev-shell programs. LYS_WM_HEADLESS=1 turns a missing
# program into a failure instead of a skip.
headless:
    LYS_WM_HEADLESS=1 go test -count=1 {{headless_packages}}

# The tests that need a real GPU, and the GLES harness tests, which the dev
# shell otherwise runs on Mesa's llvmpipe.
hardware_tests := "OnHardware|WithoutMaster|TestAStackOnARenderNode|TestDMABUFClientsOnTheGLESRenderer|TestImageCopyCaptureIntoDMABUFs|TestFullscreenDMABUFsScanOutDirectly|TestAFullscreenDMABUFScansOutOverAWallpaperAndABar|TestAnARGBDMABUFScansOutOnlyWhenItsOpaqueRegionCoversIt|TestATenBitDMABUFScansOutUnconverted|TestDestroyingAReplacedBufferStillOnScreenSendsNoRelease|TestAFullscreenSurfaceIsOfferedThePlaneFormats|TestEveryTextureFormatDrawsItsColour|TestTenBitShmBuffersShowTheirColour"

# Run the GPU tests on the host's own EGL vendors instead of the dev shell's
# Mesa, then check frame pacing: headless GLES lys-wm in a private runtime dir,
# with Xwayland off, runs es2gears_wayland and fails below min_fps.
hardware min_fps="57":
    #!/usr/bin/env bash
    set -euo pipefail
    unset __EGL_VENDOR_LIBRARY_DIRS LIBGL_ALWAYS_SOFTWARE
    LYS_WM_HEADLESS=1 go test -count=1 -v -run '{{hardware_tests}}' ./internal/render/gles/ ./internal/backend/drm/ ./internal/harness/ | grep -E '^(--- |ok|FAIL)|_test.go'
    dir=$(mktemp -d)
    trap 'rm -rf "$dir"' EXIT
    mkdir -m 0700 "$dir/run"
    go build -o "$dir/lys-wm" ./cmd/lys-wm
    printf 'xwayland:\n  enabled: false\n' > "$dir/config.yaml"
    XDG_RUNTIME_DIR="$dir/run" WAYLAND_DISPLAY= "$dir/lys-wm" -backend headless -renderer gles -config "$dir/config.yaml" 2> "$dir/lys-wm.log" &
    compositor=$!
    trap 'kill "$compositor" 2>/dev/null || true; wait "$compositor" 2>/dev/null || true; rm -rf "$dir"' EXIT
    for _ in $(seq 50); do
        socket=$(find "$dir/run" -maxdepth 1 -name 'wayland-*' ! -name '*.lock' -printf '%f\n' | head -1)
        [[ -n "$socket" ]] && break
        sleep 0.1
    done
    if [[ -z "$socket" ]]; then
        echo "hardware: lys-wm did not start" >&2
        cat "$dir/lys-wm.log" >&2
        exit 1
    fi
    XDG_RUNTIME_DIR="$dir/run" WAYLAND_DISPLAY="$socket" timeout 12 stdbuf -oL es2gears_wayland > "$dir/gears.log" 2>&1 || true
    cat "$dir/gears.log"
    fps=$(grep -oE '= [0-9.]+ FPS' "$dir/gears.log" | tail -1 | grep -oE '[0-9.]+' || true)
    if [[ -z "$fps" ]]; then
        echo "hardware: es2gears_wayland reported no frame rate" >&2
        exit 1
    fi
    echo "pacing: $fps fps on headless GLES lys-wm at 60 Hz"
    awk -v fps="$fps" -v min="{{min_fps}}" 'BEGIN { exit !(fps >= min) }' || { echo "hardware: $fps fps is below {{min_fps}}" >&2; exit 1; }

# Run a recipe in a container that sees only the dev shell's store paths,
# the checkout, its own Go caches, /dev/kvm, the GPUs' render nodes and
# NVIDIA's device nodes, and the host's GPU drivers and EGL external platforms
# (NVIDIA's GBM and Wayland support): no DRM card nodes, no
# host runtime dir, no session bus and no display sockets, so nothing it runs
# reaches the desktop session. The container user has a passwd entry, which
# Xwayland's local-user access check needs. `just docker hardware` renders on
# the host's GPU. Memory is capped, as the race suite runs many binaries.
docker recipe="gates" memory="10g":
    #!/usr/bin/env bash
    set -euo pipefail
    dir=$(mktemp -d)
    trap 'rm -rf "$dir"' EXIT
    nix print-dev-env > "$dir/env.sh"
    printf 'root:x:0:0::/root:/bin/sh\nlys-wm:x:%s:%s::/go/home:/bin/sh\n' "$(id -u)" "$(id -g)" > "$dir/passwd"
    printf 'root:x:0:\nlys-wm:x:%s:\n' "$(id -g)" > "$dir/group"
    bash=$(nix build --inputs-from . --no-link --print-out-paths nixpkgs#bash^out)/bin/bash
    certs=$(nix build --inputs-from . --no-link --print-out-paths nixpkgs#cacert^out)/etc/ssl/certs/ca-bundle.crt
    docker volume create lys-wm-go > /dev/null
    docker run --rm -v lys-wm-go:/go debian:trixie-slim chown "$(id -u):$(id -g)" /go
    gpu=()
    for node in /dev/dri/renderD* /dev/nvidia*; do
        [[ -c "$node" ]] && gpu+=(--device "$node" --group-add "$(stat -c %g "$node")")
    done
    if [[ -e /run/opengl-driver ]]; then
        gpu+=(-v /run/opengl-driver:/run/opengl-driver:ro)
    fi
    if [[ -d /etc/egl/egl_external_platform.d ]]; then
        gpu+=(-v "$(readlink -f /etc/egl/egl_external_platform.d):/etc/egl/egl_external_platform.d:ro")
    fi
    docker run --rm --init --user "$(id -u):$(id -g)" --device /dev/kvm "${gpu[@]}" \
        --memory {{quote(memory)}} --memory-swap {{quote(memory)}} --pids-limit 4096 --shm-size 1g \
        -v /nix/store:/nix/store:ro -v "$dir/env.sh:/env.sh:ro" -v "$dir/passwd:/etc/passwd:ro" -v "$dir/group:/etc/group:ro" -v "$PWD:/src" -v lys-wm-go:/go -w /src -e SSL_CERT_FILE="$certs" \
        debian:trixie-slim "$bash" -c 'source /env.sh && export HOME=/go/home GOMODCACHE=/go/mod GOCACHE=/go/cache TMPDIR=/tmp && unset XDG_RUNTIME_DIR WAYLAND_DISPLAY DISPLAY DBUS_SESSION_BUS_ADDRESS && mkdir -p "$HOME" && exec just "$1"' docker {{quote(recipe)}}

# Run a NixOS VM test that needs the network, which the Nix build sandbox
# does not give checks, in a container with /dev/kvm and its own network:
# `just vm-online vm-steam` reaches Steam's sign-in window. QEMU shows
# nothing on the desktop; screenshots land in the directory it prints.
vm-online test memory="10g":
    #!/usr/bin/env bash
    set -euo pipefail
    driver=$(nix build --no-link --print-out-paths .#{{quote(test)}}.driver)
    out=$(mktemp -d)
    echo "vm-online: screenshots in $out"
    docker run --rm --init --user "$(id -u):$(id -g)" --device /dev/kvm \
        --memory {{quote(memory)}} --memory-swap {{quote(memory)}} --pids-limit 4096 \
        -v /nix/store:/nix/store:ro -v "$out:/out" -e TMPDIR=/tmp -e HOME=/tmp \
        debian:trixie-slim "$driver/bin/nixos-test-driver" -o /out

# Fuzz the compositor with raw clients that bind every global and send
# requests with valid headers and arbitrary arguments. The gates run only the
# seed corpus; this explores for as long as asked.
fuzz duration="5m":
    LYS_WM_HEADLESS=1 go test -run='^$' -fuzz=FuzzRequests -fuzztime={{duration}} ./internal/harness/

# Run every test, headless ones included, and the patched xgb fork's under
# the race detector, which needs cgo; the binaries lys-wm ships keep
# CGO_ENABLED=0.
race:
    CGO_ENABLED=1 go test -race -count=1 ./...
    CGO_ENABLED=1 LYS_WM_HEADLESS=1 go test -race -count=1 {{headless_packages}}
    cd third_party/xgb && CGO_ENABLED=1 go test -race -count=1 ./...

# Run every release gate in order: vet, lint, test, build.
check: vet lint test build

# Static analysis of every package with go vet.
vet:
    go vet ./...

# Lint every package; settings live in .golangci.yml.
lint:
    golangci-lint run

# Run the test suite for every package.
test:
    go test ./...

# Compile-check every package; the output is discarded.
build:
    go build -o /dev/null ./...

# Regenerate the protocol bindings from the vendored XML.
generate:
    go generate ./internal/scanner

# Download the protocol XML pinned in internal/protocol/protocols.json, then
# regenerate the bindings.
protocols-fetch:
    go run ./internal/scanner -dir internal/protocol -fetch
    go generate ./internal/scanner

# Format every Go source in place with the formatters in .golangci.yml.
fmt:
    golangci-lint fmt ./...

# The formatting gate: fail, showing the diff, when any source is not
# formatted.
fmt-check:
    golangci-lint fmt --diff ./...

# Fail, showing the diff, when go.mod or go.sum is not tidy.
tidy-check:
    go mod tidy -diff

# Fail when a reachable dependency has a known vulnerability.
vuln:
    govulncheck ./...

# Move an issue to In Progress on the lys-wm project board.
start issue:
    #!/usr/bin/env bash
    set -euo pipefail
    issue={{quote(issue)}}
    if [[ ! "$issue" =~ ^[0-9]+$ ]]; then
        echo "start: issue must be a number, got '$issue'" >&2
        exit 1
    fi
    item=$(gh api graphql -F number="$issue" \
        -f query='query($number: Int!) { repository(owner: "stubbedev", name: "lys-wm") { issue(number: $number) { projectItems(first: 20) { nodes { id project { number } } } } } }' \
        --jq '.data.repository.issue.projectItems.nodes[] | select(.project.number == 3) | .id')
    if [[ -z "$item" ]]; then
        echo "start: issue #$issue is not on the lys-wm board; add it with: gh project item-add 3 --owner stubbedev --url https://github.com/stubbedev/lys-wm/issues/$issue" >&2
        exit 1
    fi
    gh project item-edit --project-id PVT_kwHOBU6Wkc4Bm521 --id "$item" \
        --field-id PVTSSF_lAHOBU6Wkc4Bm521zhlg6Ew --single-select-option-id 47fc9ee4 >/dev/null
    echo "#$issue is In Progress"
