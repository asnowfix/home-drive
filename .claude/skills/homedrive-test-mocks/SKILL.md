---
name: homedrive-test-mocks
description: Test mock conventions for homedrive — RemoteFS interface, MemFS, FlakyFS, injectable clocks, and the rule against real Google API calls in tests. Apply whenever writing or modifying tests in homedrive/.
---

# homedrive test mocks

## The rule

**No test in this repository ever calls the real Google Drive API.**

All test code uses mock implementations of `RemoteFS`. Production builds
use `RcloneFS`. Tests use `MemFS` or `FlakyFS`.

## Interface

Defined in `internal/rcloneclient/`:

```go
type RemoteFS interface {
    CopyFile(ctx context.Context, src, dstDir string) (RemoteObject, error)
    DeleteFile(ctx context.Context, path string) error
    MoveFile(ctx context.Context, src, dst string) error
    Stat(ctx context.Context, path string) (RemoteObject, error)
    ListChanges(ctx context.Context, pageToken string) (Changes, error)
    Quota(ctx context.Context) (Quota, error)
}
```

Anything that calls `RemoteFS` in production must accept it as a
parameter or struct field. Never construct `RcloneFS` directly inside
the syncer.

## MemFS

In-memory thread-safe implementation, used by 90% of tests.

```go
// internal/rcloneclient/memfs.go
type MemFS struct {
    mu      sync.Mutex
    files   map[string]MemObject
    clock   clock.Clock          // injectable
    changes []Change             // for ListChanges
    token   int64
}

func NewMemFS(opts ...MemFSOption) *MemFS

// Test helper: pre-populate
func (m *MemFS) Seed(path string, mtime time.Time, md5 string) {}
```

Properties:
- All methods thread-safe.
- `MoveFile` is O(1) (rewrite map keys), matching Drive semantics.
- `ListChanges` returns simulated `Change` events seeded by the test.
- `Quota` returns whatever the test sets via `SetQuota(used, total)`.

## FlakyFS

Decorator wrapping any `RemoteFS` to inject failures. Used for
robustness tests.

```go
type FlakyFS struct {
    inner RemoteFS
    rules []FlakyRule
}

type FlakyRule struct {
    Method string         // "CopyFile" | "*"
    Match  func(ctx, args) bool
    Inject FlakyAction    // Error | Delay | Timeout
}
```

Examples:
- "Every 3rd `CopyFile` returns network error for 5 minutes."
- "All calls timeout after the test triggers `flaky.NetworkDown()`."

## Injectable clock

Use `github.com/benbjohnson/clock` (or equivalent) for any code path
involving time.

```go
type Syncer struct {
    clock clock.Clock
    // ...
}

// In tests:
mock := clock.NewMock()
mock.Set(time.Date(2026, 4, 28, 14, 32, 0, 0, time.UTC))
syncer := NewSyncer(SyncerOpts{Clock: mock})
mock.Add(30 * time.Second)  // advance virtual time
```

Never call `time.Now()` directly in production code outside `main`.

## Embedded MQTT broker

For MQTT tests, use `github.com/mochi-mqtt/server/v2`:

```go
import mqtt "github.com/mochi-mqtt/server/v2"

func startEmbeddedBroker(t *testing.T) string {
    t.Helper()
    s := mqtt.New(nil)
    // ... configure listener on :0
    addr := s.Listeners.Get("test").Address()
    t.Cleanup(func() { s.Close() })
    return addr
}
```

Use ephemeral ports (`:0`) to avoid collisions when tests run in
parallel.

## Test file conventions

- Naming: `xxx_test.go` next to `xxx.go`.
- Test functions: `TestXxx_Case` (e.g. `TestSyncer_NewerWins_LocalNewer`).
- Table-driven where possible.
- One `t.Run(name, func(t *testing.T) {...})` per case, named by case
  description.
- Use `t.Helper()` in helpers.
- Use `t.Cleanup` over `defer` for resource cleanup in helpers.
- Use `testing.TB` for shared helpers between tests and benchmarks.

## What's allowed in tests

✅ `MemFS`, `FlakyFS`, custom in-memory mocks for tested behavior.
✅ Embedded `mochi-mqtt/server`.
✅ Real `bbolt` against `t.TempDir()` (Bolt is fast and local).
✅ Real `fsnotify` against `t.TempDir()` (Linux-only tests).

## What's forbidden in tests

❌ Real Google Drive API calls.
❌ Real production MQTT brokers.
❌ Real network calls of any kind.
❌ `time.Sleep` to wait for events; use channels with timeout instead.
❌ `os.Setenv` without `t.Setenv` (which auto-restores).

## Coverage target

> 70% per package, enforced in CI. Aim higher for `internal/syncer/` and
`internal/watcher/` — they are the most error-prone.
