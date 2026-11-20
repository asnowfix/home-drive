package config

import (
	"strings"
	"testing"
)

func TestLoad_BisyncGuard_Cases(t *testing.T) {
	tests := []struct {
		name      string
		yaml      string
		wantCount int
		wantPct   int
		wantErr   string
	}{
		{name: "omitted_uses_defaults", yaml: "local_root: /x\n", wantCount: 50, wantPct: 10},
		{name: "explicit_values", yaml: "bisync:\n  max_delete_count: 5\n  max_delete_pct: 100\n", wantCount: 5, wantPct: 100},
		{name: "partial_keeps_other_default", yaml: "bisync:\n  max_delete_pct: 25\n", wantCount: 50, wantPct: 25},
		{name: "count_zero_rejected", yaml: "bisync:\n  max_delete_count: 0\n", wantErr: "max_delete_count"},
		{name: "count_negative_rejected", yaml: "bisync:\n  max_delete_count: -3\n", wantErr: "max_delete_count"},
		{name: "pct_zero_rejected", yaml: "bisync:\n  max_delete_pct: 0\n", wantErr: "max_delete_pct"},
		{name: "pct_above_100_rejected", yaml: "bisync:\n  max_delete_pct: 101\n", wantErr: "max_delete_pct"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Load(writeConfig(t, tc.yaml))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want one mentioning %s", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if *cfg.Bisync.MaxDeleteCount != tc.wantCount || *cfg.Bisync.MaxDeletePct != tc.wantPct {
				t.Errorf("guard = %d, %d, want %d, %d",
					*cfg.Bisync.MaxDeleteCount, *cfg.Bisync.MaxDeletePct, tc.wantCount, tc.wantPct)
			}
		})
	}
}
