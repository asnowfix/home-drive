package rcloneclient

import (
	"sync"
	"testing"
)

func TestIDPathCache_GetPut_Cases(t *testing.T) {
	cases := []struct {
		name    string
		seed    map[string]string
		lookup  string
		wantOK  bool
		wantVal string
	}{
		{"found", map[string]string{"id1": "dir/file.txt"}, "id1", true, "dir/file.txt"},
		{"not found", map[string]string{"id1": "dir/file.txt"}, "id2", false, ""},
		{"empty id never stored", map[string]string{"": "root"}, "", false, ""},
		{"root path is valid value", map[string]string{"root-id": ""}, "root-id", true, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newIDPathCache()
			for id, p := range tc.seed {
				c.put(id, p)
			}
			got, ok := c.get(tc.lookup)
			if ok != tc.wantOK || got != tc.wantVal {
				t.Errorf("get(%q) = (%q, %v), want (%q, %v)", tc.lookup, got, ok, tc.wantVal, tc.wantOK)
			}
		})
	}
}

func TestIDPathCache_Overwrite(t *testing.T) {
	c := newIDPathCache()
	c.put("id1", "old/path.txt")
	c.put("id1", "new/path.txt")

	got, ok := c.get("id1")
	if !ok || got != "new/path.txt" {
		t.Errorf("get(id1) = (%q, %v), want (\"new/path.txt\", true)", got, ok)
	}
	if c.len() != 1 {
		t.Errorf("len() = %d, want 1", c.len())
	}
}

func TestIDPathCache_ConcurrentAccess(t *testing.T) {
	c := newIDPathCache()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func(n int) {
			defer wg.Done()
			c.put("id", "path")
		}(i)
		go func(n int) {
			defer wg.Done()
			c.get("id")
		}(i)
	}
	wg.Wait()
}
