package rcloneclient

import (
	"context"
	"net/http"
	"testing"

	drivev3 "google.golang.org/api/drive/v3"
)

func TestPollChanges_FolderMimeTypeSetsIsDir(t *testing.T) {
	tests := []struct {
		name     string
		mimeType string
		wantDir  bool
	}{
		{"Folder", "application/vnd.google-apps.folder", true},
		{"PlainFile", "text/plain", false},
		{"Shortcut", "application/vnd.google-apps.shortcut", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newTestDriveService(t, func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(t, w, &drivev3.ChangeList{
					NewStartPageToken: "next-token",
					Changes: []*drivev3.Change{{
						FileId: "id-1",
						File: &drivev3.File{
							Id: "id-1", Name: "hd-test",
							Parents:  []string{"root-id"},
							MimeType: tt.mimeType,
						},
					}},
				})
			})
			r := newTestRcloneFS(svc)
			r.rootID = "root-id"
			r.pathCache.put("root-id", "")

			changes, err := r.pollChanges(context.Background(), "start-token")
			if err != nil {
				t.Fatalf("pollChanges: %v", err)
			}
			if len(changes.Items) != 1 || changes.Items[0].Object == nil {
				t.Fatalf("Items = %+v, want 1 item with Object", changes.Items)
			}
			if got := changes.Items[0].Object.IsDir; got != tt.wantDir {
				t.Errorf("IsDir = %v, want %v", got, tt.wantDir)
			}
		})
	}
}
