package syncer

import "testing"

func TestOp_String(t *testing.T) {
	tests := []struct {
		op   Op
		want string
	}{
		{OpCreate, "create"},
		{OpWrite, "write"},
		{OpRemove, "remove"},
		{OpRename, "rename"},
		{Op(99), "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.op.String(); got != tt.want {
				t.Errorf("Op(%d).String() = %q, want %q", tt.op, got, tt.want)
			}
		})
	}
}
