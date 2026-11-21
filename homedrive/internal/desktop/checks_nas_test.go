package desktop

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckNASOverlap_Cases(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "gdrive")
	mustMkdir(t, root)
	link := filepath.Join(dir, "link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		mp    string
		nas   *NASInfo
		wantE error
	}{
		{"NoNASConfig", root, nil, nil},
		{"EmptyLocalRoot", root, &NASInfo{}, nil},
		{"Unrelated", filepath.Join(dir, "GoogleDrive"), &NASInfo{LocalRoot: root}, nil},
		{"SiblingWithSamePrefix", root + "2", &NASInfo{LocalRoot: root}, nil},
		{"Equal", root, &NASInfo{LocalRoot: root}, ErrOverlapsNAS},
		{"InsideRoot", filepath.Join(root, "sub", "mp"), &NASInfo{LocalRoot: root}, ErrOverlapsNAS},
		{"ContainsRoot", dir, &NASInfo{LocalRoot: root}, ErrOverlapsNAS},
		{"ViaSymlink", link, &NASInfo{LocalRoot: root}, ErrOverlapsNAS},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckNASOverlap(tc.mp, tc.nas)
			if !errors.Is(err, tc.wantE) {
				t.Fatalf("err = %v, want %v", err, tc.wantE)
			}
		})
	}
}

func TestCheckTokenNotNAS_Cases(t *testing.T) {
	const def = "/home/u/.config/rclone/rclone.conf"
	tests := []struct {
		name  string
		token string
		nas   *NASInfo
		wantE error
	}{
		{"NoNASConfig", "/t/rclone.conf", nil, nil},
		{"Different", "/t/rclone.conf", &NASInfo{RcloneConfig: "/n/rclone.conf"}, nil},
		{"Same", "/t/rclone.conf", &NASInfo{RcloneConfig: "/t/rclone.conf"}, ErrTokenIsNASConfig},
		{"SameAfterClean", "/t/x/../rclone.conf", &NASInfo{RcloneConfig: "/t/rclone.conf"}, ErrTokenIsNASConfig},
		{"NASUnsetUsesRcloneDefault", def, &NASInfo{}, ErrTokenIsNASConfig},
		{"NASUnsetOtherToken", "/t/rclone.conf", &NASInfo{}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckTokenNotNAS(tc.token, tc.nas, def)
			if !errors.Is(err, tc.wantE) {
				t.Fatalf("err = %v, want %v", err, tc.wantE)
			}
		})
	}
}

func TestLoadNASInfo_Cases(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "config.yaml")
	bad := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(good, []byte("local_root: /r\nrclone_config: /c\nremote: x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, []byte("local_root: [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	n, err := LoadNASInfo(good)
	if err != nil || n == nil || n.LocalRoot != "/r" || n.RcloneConfig != "/c" {
		t.Fatalf("good: %+v, %v", n, err)
	}
	if n, err := LoadNASInfo(filepath.Join(dir, "missing.yaml")); n != nil || err != nil {
		t.Fatalf("missing: %+v, %v", n, err)
	}
	if _, err := LoadNASInfo(bad); err == nil {
		t.Fatal("unparsable NAS config must be an error")
	}
	if _, err := LoadNASInfo(dir); err == nil {
		t.Fatal("unreadable NAS config must be an error")
	}
}
