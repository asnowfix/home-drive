# Development environment

`homedrive` is a Linux-target project (inotify, systemd) developed
primarily on macOS workstations. Watcher tests **must** run on Linux to
exercise real inotify behavior — fsnotify on macOS uses FSEvents with
different event semantics.

See [`PLAN.md` §19](../PLAN.md) for the full setup. Quick reference:

## OrbStack (Apple Silicon Macs)

```bash
brew install orbstack
orb create ubuntu-24.04 dev
orb run -m dev -- bash -c 'cd ~/code/home-drive && go test ./homedrive/...'
```

## Cross-compilation

```bash
GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" \
  -o dist/homedrive-arm64 ./homedrive/cmd/homedrive
```

## Make targets (after Phase 0)

```make
build-mac        # local Mac build (sanity check)
build-arm64      # cross-compile for the Pi
build-amd64      # cross-compile for x86_64 Linux
test-linux       # tests inside OrbStack VM
test-pi          # tests via SSH on the production Pi
deploy-pi        # build + scp + systemctl restart
```

## VS Code

`.vscode/settings.json`:
```json
{
  "go.testEnvVars": { "HOMEDRIVE_LOG": "stderr" },
  "go.testFlags": ["-v", "-race"],
  "go.toolsEnvVars": { "GOOS": "linux", "GOARCH": "arm64" },
  "gopls": { "build.env": { "GOOS": "linux" } }
}
```

This makes gopls type-check the Linux build, catching macOS-only code
paths at edit time.

## Build tags

Linux-only files:
```go
//go:build linux

package watcher
```

Tests requiring inotify cookies:
```go
if runtime.GOOS != "linux" {
    t.Skip("requires Linux inotify cookies")
}
```
