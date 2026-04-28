package rcloneclient

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestFlakyFS_ErrorInjection(t *testing.T) {
	t.Parallel()

	inner := NewMemFS()
	inner.Seed("file.txt", time.Now(), "hash")
	ctx := context.Background()

	tests := []struct {
		name    string
		rule    FlakyRule
		method  string
		wantErr error
	}{
		{
			name: "CopyFileError",
			rule: FlakyRule{
				Method: "CopyFile",
				Action: FlakyAction{Err: ErrNetworkUnavailable},
			},
			method:  "CopyFile",
			wantErr: ErrNetworkUnavailable,
		},
		{
			name: "DeleteFileError",
			rule: FlakyRule{
				Method: "DeleteFile",
				Action: FlakyAction{Err: ErrPermissionDenied},
			},
			method:  "DeleteFile",
			wantErr: ErrPermissionDenied,
		},
		{
			name: "MoveFileError",
			rule: FlakyRule{
				Method: "MoveFile",
				Action: FlakyAction{Err: ErrQuotaExhausted},
			},
			method:  "MoveFile",
			wantErr: ErrQuotaExhausted,
		},
		{
			name: "StatError",
			rule: FlakyRule{
				Method: "Stat",
				Action: FlakyAction{Err: ErrNotFound},
			},
			method:  "Stat",
			wantErr: ErrNotFound,
		},
		{
			name: "ListChangesError",
			rule: FlakyRule{
				Method: "ListChanges",
				Action: FlakyAction{Err: ErrOAuthExpired},
			},
			method:  "ListChanges",
			wantErr: ErrOAuthExpired,
		},
		{
			name: "QuotaError",
			rule: FlakyRule{
				Method: "Quota",
				Action: FlakyAction{Err: ErrNetworkUnavailable},
			},
			method:  "Quota",
			wantErr: ErrNetworkUnavailable,
		},
		{
			name: "WildcardError",
			rule: FlakyRule{
				Method: "*",
				Action: FlakyAction{Err: ErrNetworkUnavailable},
			},
			method:  "Stat",
			wantErr: ErrNetworkUnavailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := NewFlakyFS(inner, tt.rule)
			var err error

			switch tt.method {
			case "CopyFile":
				_, err = f.CopyFile(ctx, "/local/file.txt", "remote")
			case "DeleteFile":
				err = f.DeleteFile(ctx, "file.txt")
			case "MoveFile":
				err = f.MoveFile(ctx, "file.txt", "moved.txt")
			case "Stat":
				_, err = f.Stat(ctx, "file.txt")
			case "ListChanges":
				_, err = f.ListChanges(ctx, "")
			case "Quota":
				_, err = f.Quota(ctx)
			}

			if !errors.Is(err, tt.wantErr) {
				t.Errorf("%s error = %v, want %v", tt.method, err, tt.wantErr)
			}
		})
	}
}

func TestFlakyFS_Passthrough(t *testing.T) {
	t.Parallel()

	inner := NewMemFS()
	inner.Seed("existing.txt", time.Now(), "hash")
	ctx := context.Background()

	// No rules -- should pass through to inner.
	f := NewFlakyFS(inner)

	obj, err := f.Stat(ctx, "existing.txt")
	if err != nil {
		t.Fatalf("Stat passthrough error: %v", err)
	}
	if obj.Path != "existing.txt" {
		t.Errorf("Stat path = %q, want %q", obj.Path, "existing.txt")
	}
}

func TestFlakyFS_MatchFunction(t *testing.T) {
	t.Parallel()

	inner := NewMemFS()
	inner.Seed("safe.txt", time.Now(), "h1")
	inner.Seed("dangerous.txt", time.Now(), "h2")
	ctx := context.Background()

	// Only fail on "dangerous.txt".
	f := NewFlakyFS(inner, FlakyRule{
		Method: "Stat",
		Match: func(_, firstArg string) bool {
			return firstArg == "dangerous.txt"
		},
		Action: FlakyAction{Err: ErrPermissionDenied},
	})

	// safe.txt should work.
	_, err := f.Stat(ctx, "safe.txt")
	if err != nil {
		t.Errorf("Stat(safe.txt) should pass through: %v", err)
	}

	// dangerous.txt should fail.
	_, err = f.Stat(ctx, "dangerous.txt")
	if !errors.Is(err, ErrPermissionDenied) {
		t.Errorf("Stat(dangerous.txt) error = %v, want ErrPermissionDenied", err)
	}
}

func TestFlakyFS_Timeout(t *testing.T) {
	t.Parallel()

	inner := NewMemFS()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	f := NewFlakyFS(inner, FlakyRule{
		Method: "CopyFile",
		Action: FlakyAction{Timeout: true},
	})

	_, err := f.CopyFile(ctx, "/local/file.txt", "remote")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("CopyFile with timeout: got %v, want context.DeadlineExceeded", err)
	}
}

func TestFlakyFS_SetRules(t *testing.T) {
	t.Parallel()

	inner := NewMemFS()
	inner.Seed("file.txt", time.Now(), "hash")
	ctx := context.Background()

	f := NewFlakyFS(inner)

	// Initially no rules -- Stat should work.
	_, err := f.Stat(ctx, "file.txt")
	if err != nil {
		t.Fatalf("Stat before rules: %v", err)
	}

	// Add a rule.
	f.SetRules(FlakyRule{
		Method: "*",
		Action: FlakyAction{Err: ErrNetworkUnavailable},
	})

	_, err = f.Stat(ctx, "file.txt")
	if !errors.Is(err, ErrNetworkUnavailable) {
		t.Errorf("Stat after SetRules: got %v, want ErrNetworkUnavailable", err)
	}

	// Clear rules.
	f.ClearRules()
	_, err = f.Stat(ctx, "file.txt")
	if err != nil {
		t.Errorf("Stat after ClearRules: %v", err)
	}
}

func TestFlakyFS_Delay(t *testing.T) {
	t.Parallel()

	inner := NewMemFS()
	inner.Seed("file.txt", time.Now(), "hash")
	ctx := context.Background()

	f := NewFlakyFS(inner, FlakyRule{
		Method: "Stat",
		Action: FlakyAction{Delay: 10 * time.Millisecond},
	})

	start := time.Now()
	_, err := f.Stat(ctx, "file.txt")
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Stat with delay: %v", err)
	}
	if elapsed < 10*time.Millisecond {
		t.Errorf("Stat completed too fast: %v, want >= 10ms", elapsed)
	}
}

func TestFlakyFS_AllMethodsPassthrough(t *testing.T) {
	t.Parallel()

	inner := NewMemFS()
	inner.Seed("src.txt", time.Now(), "hash")
	ctx := context.Background()

	// No rules -- all methods should pass through.
	f := NewFlakyFS(inner)

	// CopyFile passthrough.
	obj, err := f.CopyFile(ctx, "/local/new.txt", "remote")
	if err != nil {
		t.Errorf("CopyFile passthrough: %v", err)
	}
	if obj.Path != "remote/new.txt" {
		t.Errorf("CopyFile path = %q, want %q", obj.Path, "remote/new.txt")
	}

	// DeleteFile passthrough.
	err = f.DeleteFile(ctx, "src.txt")
	if err != nil {
		t.Errorf("DeleteFile passthrough: %v", err)
	}

	// MoveFile passthrough -- seed a new file first.
	inner.Seed("mv_src.txt", time.Now(), "hash2")
	err = f.MoveFile(ctx, "mv_src.txt", "mv_dst.txt")
	if err != nil {
		t.Errorf("MoveFile passthrough: %v", err)
	}

	// ListChanges passthrough.
	inner.AddChange(Change{Path: "test.txt"})
	ch, err := f.ListChanges(ctx, "")
	if err != nil {
		t.Errorf("ListChanges passthrough: %v", err)
	}
	if len(ch.Items) == 0 {
		t.Error("ListChanges passthrough: expected items")
	}

	// Quota passthrough.
	q, err := f.Quota(ctx)
	if err != nil {
		t.Errorf("Quota passthrough: %v", err)
	}
	if q.Total <= 0 {
		t.Error("Quota passthrough: expected positive total")
	}
}

func TestFlakyFS_DelayWithCancel(t *testing.T) {
	t.Parallel()

	inner := NewMemFS()
	inner.Seed("file.txt", time.Now(), "hash")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	f := NewFlakyFS(inner, FlakyRule{
		Method: "Stat",
		Action: FlakyAction{Delay: 5 * time.Second},
	})

	_, err := f.Stat(ctx, "file.txt")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Stat with long delay + short context: got %v, want context.DeadlineExceeded", err)
	}
}
