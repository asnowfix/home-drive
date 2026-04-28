package rcloneclient

import (
	"testing"
)

// Compile-time interface compliance checks. These ensure that all
// implementations satisfy RemoteFS without requiring a running rclone
// backend or network access.
var (
	_ RemoteFS = (*MemFS)(nil)
	_ RemoteFS = (*FlakyFS)(nil)
	_ RemoteFS = (*DryRunFS)(nil)
	// RcloneFS is verified separately -- it requires rclone config
	// and is the only implementation that calls real rclone libraries.
	_ RemoteFS = (*RcloneFS)(nil)
)

func TestQuota_UsedPercent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		q    Quota
		want float64
	}{
		{
			name: "Normal",
			q:    Quota{Used: 50, Total: 100},
			want: 50.0,
		},
		{
			name: "Full",
			q:    Quota{Used: 100, Total: 100},
			want: 100.0,
		},
		{
			name: "Empty",
			q:    Quota{Used: 0, Total: 100},
			want: 0.0,
		},
		{
			name: "UnlimitedNegative",
			q:    Quota{Used: 50, Total: -1},
			want: 0.0,
		},
		{
			name: "ZeroTotal",
			q:    Quota{Used: 0, Total: 0},
			want: 0.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.q.UsedPercent()
			if got != tt.want {
				t.Errorf("UsedPercent() = %f, want %f", got, tt.want)
			}
		})
	}
}
