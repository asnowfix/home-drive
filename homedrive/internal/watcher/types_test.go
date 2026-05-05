package watcher

import "testing"

func TestOp_String(t *testing.T) {
	tests := []struct {
		name string
		op   Op
		want string
	}{
		{name: "Create", op: OpCreate, want: "create"},
		{name: "Write", op: OpWrite, want: "write"},
		{name: "Remove", op: OpRemove, want: "remove"},
		{name: "Rename", op: OpRename, want: "rename"},
		{name: "DirRename", op: OpDirRename, want: "dir_rename"},
		{name: "Unknown", op: Op(99), want: "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.op.String(); got != tt.want {
				t.Errorf("Op(%d).String() = %q, want %q", tt.op, got, tt.want)
			}
		})
	}
}
