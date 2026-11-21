---
name: homedrive-rclone-import
description: Rules for importing rclone packages and calling rclone from Go in homedrive. Apply whenever adding rclone dependencies, writing the rclone wrapper, or implementing remote filesystem operations.
---

# Importing rclone in homedrive

Goal: keep the build lean (it must run on a Raspberry Pi 3B+ alongside other
workloads) by registering only the Drive backend, and
keep all rclone calls behind a stable wrapper interface.

## Allow-list

Only these rclone packages may be imported anywhere in `homedrive/`:

```go
import (
    _ "github.com/rclone/rclone/backend/drive"   // single backend registered
    "github.com/rclone/rclone/fs"
    "github.com/rclone/rclone/fs/config"
    "github.com/rclone/rclone/fs/config/configfile"
    "github.com/rclone/rclone/fs/operations"
    "github.com/rclone/rclone/fs/sync"
)
```

**Desktop mode only.** `homedrive/internal/desktop/` and
`homedrive/cmd/homedrive/mount*.go` (`homedrive mount`, design
`docs/design/desktop-mode.md` section 6) may additionally import:

```go
import (
    "github.com/rclone/rclone/vfs"
    "github.com/rclone/rclone/vfs/vfscommon"
    "github.com/rclone/rclone/cmd/mountlib"
    _ "github.com/rclone/rclone/cmd/mount2"
    "github.com/rclone/rclone/fs/rc"
)
```

A CI grep (`.github/workflows/homedrive.yml`) fails on any import of `vfs*`,
`cmd/*` or `fs/rc*` outside those paths. There is still exactly one backend
(`backend/drive`); test files may import `backend/local` for in-process FUSE
tests. In desktop code the `internal/rcloneclient`-only wrapper rule below does
not apply: `internal/desktop` talks to vfs and mountlib directly.

Adding **any** other rclone package requires:
1. User approval.
2. The measured `linux/arm64` stripped binary size before and after.
3. A note in PLAN.md §10 documenting why and the size impact.

## Wrapper pattern

`internal/rcloneclient/` is the **only** package allowed to import rclone.
Everything else uses the `RemoteFS` interface defined there.

```go
// internal/rcloneclient/remotefs.go
type RemoteFS interface {
    CopyFile(ctx context.Context, src, dstDir string) (RemoteObject, error)
    DeleteFile(ctx context.Context, path string) error
    MoveFile(ctx context.Context, src, dst string) error
    Stat(ctx context.Context, path string) (RemoteObject, error)
    ListChanges(ctx context.Context, pageToken string) (Changes, error)
    Quota(ctx context.Context) (Quota, error)
}
```

Rules:
- `RemoteFS` is the contract; `RcloneFS` is the production impl.
- The syncer (`internal/syncer/`) takes a `RemoteFS`, never an `*rclone.Fs`.
- Tests use `MemFS` and `FlakyFS` (see `homedrive-test-mocks`).

## Method-to-rclone mapping

| Wrapper method | rclone call |
|---|---|
| `CopyFile(ctx, srcLocal, dstRemoteDir)` | `operations.CopyFile` |
| `DeleteFile(ctx, remotePath)` | `operations.DeleteFile` |
| `MoveFile(ctx, srcRemote, dstRemote)` | `operations.MoveFile` |
| `Stat(ctx, remotePath)` | `fs.NewObject` |
| `ListChanges(ctx, pageToken)` | direct Drive API via cast to `*drive.Fs` |
| `Quota(ctx)` | `fs.About` |

## Loading rclone.conf

```go
import "github.com/rclone/rclone/fs/config/configfile"

func init() {
    configfile.Install()
}

// At startup, after reading homedrive config:
fs.GlobalConfig.ConfigPath = cfg.RcloneConfig  // /home/<user>/.config/rclone/rclone.conf
```

## Obtaining a typed `*drive.Fs` for Changes API

`operations` doesn't expose `changes.list`. To use the Drive Changes API:

```go
import (
    rclonefs "github.com/rclone/rclone/fs"
    "github.com/rclone/rclone/backend/drive"
)

fsObj, err := rclonefs.NewFs(ctx, "gdrive:")
if err != nil {
    return err
}
driveFs, ok := fsObj.(*drive.Fs)
if !ok {
    return fmt.Errorf("expected *drive.Fs, got %T", fsObj)
}
// driveFs has methods to call the Drive Changes API directly.
```

Document this cast in a comment — it's the only place where the wrapper
abstraction leaks.

## Dry-run

The wrapper honors `--dry-run` at its layer:

```go
func (r *RcloneFS) CopyFile(ctx context.Context, src, dstDir string) (RemoteObject, error) {
    if r.dryRun {
        r.log.Info("would copy", "src", src, "dst", dstDir)
        return RemoteObject{Path: dstDir + "/" + filepath.Base(src), DryRun: true}, nil
    }
    // real call
}
```

Dry-run also disables store writes so re-runs replay the same plan.

## Verification (mandatory after every build)

```bash
# Binary size (reported, no limit)
du -h homedrive

# Only "drive" may be explicitly imported anywhere in source. crypt is a
# transitive dependency of drive itself, so a raw go tool nm binary symbol
# count is not a reliable check -- verify at the source-import level.
imports=$(grep -rn '"github.com/rclone/rclone/backend/' . --include='*.go' \
  | grep -v '_test.go' | grep -v 'rclone/backend/drive' || true)
test -z "$imports"
```

CI enforces both. Local builds should run these too.

## Build flags

Always:
```bash
go build -trimpath -ldflags="-s -w" -o homedrive ./cmd/homedrive
```

`-trimpath` for reproducibility; `-s -w` to strip debug symbols.

## OAuth health

The wrapper exposes OAuth health to the syncer:

```go
type OAuthStatus struct {
    Healthy bool
    NextRefresh time.Time
    LastError error
}

func (r *RcloneFS) OAuth() OAuthStatus { ... }
```

The syncer publishes `oauth.refresh_failed` on MQTT and returns 503 from
`/healthz` when `Healthy == false`.
