package tokenlock

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAcquire_Contention(t *testing.T) {
	tests := []struct {
		name    string
		release bool
		wantErr bool
	}{
		{"SecondAcquireWhileHeld", false, true},
		{"SecondAcquireAfterRelease", true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			token := filepath.Join(t.TempDir(), "sub", "rclone.conf")
			first, err := Acquire(token)
			if err != nil {
				t.Fatalf("first Acquire: %v", err)
			}
			t.Cleanup(func() { _ = first.Release() })
			if tc.release {
				if err := first.Release(); err != nil {
					t.Fatalf("Release: %v", err)
				}
			}
			second, err := Acquire(token)
			if tc.wantErr {
				if !errors.Is(err, ErrHeld) {
					t.Fatalf("second Acquire error = %v, want ErrHeld", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("second Acquire: %v", err)
			}
			_ = second.Release()
		})
	}
}

func TestAcquire_LockFileLocation(t *testing.T) {
	token := filepath.Join(t.TempDir(), "rclone.conf")
	l, err := Acquire(token)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Release() }()
	if _, err := os.Stat(Path(token)); err != nil {
		t.Fatalf("lock file %q missing: %v", Path(token), err)
	}
	if _, err := os.Stat(token); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("token file itself must not be created, stat err = %v", err)
	}
}

func TestAcquire_Errors(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(filepath.Join(blocker, "rclone.conf")); err == nil {
		t.Fatal("Acquire under a regular file must fail")
	}
}

func TestRelease_NilAndIdempotent(t *testing.T) {
	var nilLock *Lock
	if err := nilLock.Release(); err != nil {
		t.Fatalf("nil Release: %v", err)
	}
	l, err := Acquire(filepath.Join(t.TempDir(), "rclone.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
	if err := l.Release(); err != nil {
		t.Fatalf("second Release: %v", err)
	}
}
