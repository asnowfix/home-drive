package rcloneclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	drivev3 "google.golang.org/api/drive/v3"
	"google.golang.org/api/option"

	"golang.org/x/oauth2"
)

// newTestDriveService builds a *drivev3.Service pointed at a local
// httptest.Server so tests never call the real Google Drive API (per
// homedrive-test-mocks: no real Google API calls in tests).
func newTestDriveService(t *testing.T, handler http.HandlerFunc) *drivev3.Service {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	svc, err := drivev3.NewService(context.Background(),
		option.WithoutAuthentication(),
		option.WithHTTPClient(srv.Client()),
		option.WithEndpoint(srv.URL),
	)
	if err != nil {
		t.Fatalf("build test drive service: %v", err)
	}
	return svc
}

func newTestRcloneFS(svc *drivev3.Service) *RcloneFS {
	return &RcloneFS{
		remote:     "gdrive:",
		remoteName: "gdrive",
		log:        slog.Default(),
		changesSvc: svc,
		pathCache:  newIDPathCache(),
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

func TestGetStartPageToken_ReturnsPrefixedRealToken(t *testing.T) {
	svc := newTestDriveService(t, func(w http.ResponseWriter, req *http.Request) {
		if !strings.Contains(req.URL.Path, "changes/startPageToken") {
			t.Fatalf("unexpected request path %q", req.URL.Path)
		}
		writeJSON(t, w, &drivev3.StartPageToken{StartPageToken: "12345"})
	})

	r := newTestRcloneFS(svc)
	tok, err := r.GetStartPageToken(context.Background())
	if err != nil {
		t.Fatalf("GetStartPageToken: %v", err)
	}
	if tok != initialSyncPrefix+"12345" {
		t.Errorf("token = %q, want %q", tok, initialSyncPrefix+"12345")
	}
	if strings.Contains(tok, "initial_sync") == false {
		t.Errorf("expected token to still carry the full-walk marker")
	}
}

func TestPollChanges_IncrementalChanges(t *testing.T) {
	fileMTime := "2026-07-20T10:00:00Z"
	requestCount := 0

	svc := newTestDriveService(t, func(w http.ResponseWriter, req *http.Request) {
		requestCount++
		list := &drivev3.ChangeList{
			NewStartPageToken: "next-token",
			Changes: []*drivev3.Change{
				{
					FileId: "file-1",
					File: &drivev3.File{
						Id:           "file-1",
						Name:         "report.txt",
						Parents:      []string{"root-id"},
						MimeType:     "text/plain",
						ModifiedTime: fileMTime,
						Md5Checksum:  "abc123",
						Size:         42,
					},
				},
			},
		}
		writeJSON(t, w, list)
	})

	r := newTestRcloneFS(svc)
	r.rootID = "root-id"
	r.pathCache.put("root-id", "")

	changes, err := r.pollChanges(context.Background(), "start-token")
	if err != nil {
		t.Fatalf("pollChanges: %v", err)
	}
	if requestCount != 1 {
		t.Fatalf("requestCount = %d, want 1", requestCount)
	}
	if changes.NextPageToken != "next-token" {
		t.Errorf("NextPageToken = %q, want %q", changes.NextPageToken, "next-token")
	}
	if len(changes.Items) != 1 {
		t.Fatalf("len(Items) = %d, want 1", len(changes.Items))
	}
	got := changes.Items[0]
	if got.Path != "report.txt" || got.Deleted {
		t.Errorf("Items[0] = %+v, want path=report.txt deleted=false", got)
	}
	if got.Object == nil || got.Object.MD5 != "abc123" || got.Object.Size != 42 {
		t.Errorf("Items[0].Object = %+v, want MD5=abc123 Size=42", got.Object)
	}
	wantMTime, _ := time.Parse(time.RFC3339, fileMTime)
	if !got.Object.ModTime.Equal(wantMTime) {
		t.Errorf("ModTime = %v, want %v", got.Object.ModTime, wantMTime)
	}
}

func TestPollChanges_NestedFileUsesCachedParent(t *testing.T) {
	svc := newTestDriveService(t, func(w http.ResponseWriter, req *http.Request) {
		writeJSON(t, w, &drivev3.ChangeList{
			NewStartPageToken: "next",
			Changes: []*drivev3.Change{{
				FileId: "file-2",
				File: &drivev3.File{
					Id:       "file-2",
					Name:     "photo.jpg",
					Parents:  []string{"folder-a"},
					MimeType: "image/jpeg",
				},
			}},
		})
	})

	r := newTestRcloneFS(svc)
	r.rootID = "root-id"
	r.pathCache.put("root-id", "")
	r.pathCache.put("folder-a", "Pictures/2026")

	changes, err := r.pollChanges(context.Background(), "tok")
	if err != nil {
		t.Fatalf("pollChanges: %v", err)
	}
	if len(changes.Items) != 1 || changes.Items[0].Path != "Pictures/2026/photo.jpg" {
		t.Fatalf("Items = %+v, want single item Pictures/2026/photo.jpg", changes.Items)
	}
}

func TestPollChanges_DeletionCases(t *testing.T) {
	cases := []struct {
		name   string
		change *drivev3.Change
		seed   map[string]string
		want   []Change // nil means skipped
	}{
		{
			name:   "removed with cached path",
			change: &drivev3.Change{FileId: "gone-1", Removed: true},
			seed:   map[string]string{"gone-1": "old/report.txt"},
			want:   []Change{{Path: "old/report.txt", Deleted: true}},
		},
		{
			name:   "removed without cached path is skipped",
			change: &drivev3.Change{FileId: "unknown-1", Removed: true},
			seed:   nil,
			want:   nil,
		},
		{
			name: "trashed file resolves via parent and is deleted",
			change: &drivev3.Change{
				FileId: "trashed-1",
				File: &drivev3.File{
					Id: "trashed-1", Name: "old.txt",
					Parents: []string{"root-id"}, Trashed: true,
				},
			},
			seed: nil,
			want: []Change{{Path: "old.txt", Deleted: true}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := newTestDriveService(t, func(w http.ResponseWriter, req *http.Request) {
				writeJSON(t, w, &drivev3.ChangeList{
					NewStartPageToken: "next",
					Changes:           []*drivev3.Change{tc.change},
				})
			})
			r := newTestRcloneFS(svc)
			r.rootID = "root-id"
			r.pathCache.put("root-id", "")
			for id, p := range tc.seed {
				r.pathCache.put(id, p)
			}

			changes, err := r.pollChanges(context.Background(), "tok")
			if err != nil {
				t.Fatalf("pollChanges: %v", err)
			}
			if len(changes.Items) != len(tc.want) {
				t.Fatalf("Items = %+v, want %+v", changes.Items, tc.want)
			}
			for i, w := range tc.want {
				got := changes.Items[i]
				if got.Path != w.Path || got.Deleted != w.Deleted {
					t.Errorf("Items[%d] = %+v, want %+v", i, got, w)
				}
			}
		})
	}
}

func TestPollChanges_ExcludedPathSkipped(t *testing.T) {
	svc := newTestDriveService(t, func(w http.ResponseWriter, req *http.Request) {
		writeJSON(t, w, &drivev3.ChangeList{
			NewStartPageToken: "next",
			Changes: []*drivev3.Change{{
				FileId: "f1",
				File:   &drivev3.File{Id: "f1", Name: "cache.tmp", Parents: []string{"root-id"}},
			}},
		})
	})
	r := newTestRcloneFS(svc)
	r.exclude = []string{"*.tmp"}
	r.rootID = "root-id"
	r.pathCache.put("root-id", "")

	changes, err := r.pollChanges(context.Background(), "tok")
	if err != nil {
		t.Fatalf("pollChanges: %v", err)
	}
	if len(changes.Items) != 0 {
		t.Errorf("Items = %+v, want empty (excluded)", changes.Items)
	}
}

func TestPollChanges_Pagination(t *testing.T) {
	var seenTokens []string
	svc := newTestDriveService(t, func(w http.ResponseWriter, req *http.Request) {
		tok := req.URL.Query().Get("pageToken")
		seenTokens = append(seenTokens, tok)

		if tok == "page-1" {
			writeJSON(t, w, &drivev3.ChangeList{
				NextPageToken: "page-2",
				Changes: []*drivev3.Change{{
					FileId: "a", File: &drivev3.File{Id: "a", Name: "a.txt", Parents: []string{"root-id"}},
				}},
			})
			return
		}
		writeJSON(t, w, &drivev3.ChangeList{
			NewStartPageToken: "final-token",
			Changes: []*drivev3.Change{{
				FileId: "b", File: &drivev3.File{Id: "b", Name: "b.txt", Parents: []string{"root-id"}},
			}},
		})
	})

	r := newTestRcloneFS(svc)
	r.rootID = "root-id"
	r.pathCache.put("root-id", "")

	changes, err := r.pollChanges(context.Background(), "page-1")
	if err != nil {
		t.Fatalf("pollChanges: %v", err)
	}
	if len(seenTokens) != 2 || seenTokens[0] != "page-1" || seenTokens[1] != "page-2" {
		t.Errorf("seenTokens = %v, want [page-1 page-2]", seenTokens)
	}
	if changes.NextPageToken != "final-token" {
		t.Errorf("NextPageToken = %q, want final-token", changes.NextPageToken)
	}
	if len(changes.Items) != 2 {
		t.Errorf("len(Items) = %d, want 2", len(changes.Items))
	}
}

func TestPollChanges_410Gone_ReturnsErrGone(t *testing.T) {
	svc := newTestDriveService(t, func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusGone)
		_, _ = fmt.Fprint(w, `{"error":{"code":410,"message":"Invalid page token"}}`)
	})

	r := newTestRcloneFS(svc)
	r.rootID = "root-id"
	r.pathCache.put("root-id", "")

	_, err := r.pollChanges(context.Background(), "stale-token")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, ErrGone) {
		t.Errorf("expected error wrapping ErrGone, got: %v", err)
	}
	// Pin the 410 and 400 cases as distinguishable (issue #64 PR review
	// item 4): the classic 410 path must NOT also carry ErrTokenRejected,
	// which marks the non-410 branch exclusively.
	if errors.Is(err, ErrTokenRejected) {
		t.Errorf("410 case must not wrap ErrTokenRejected, got: %v", err)
	}
}

func TestPollChanges_403RateLimited_DoesNotResetToken(t *testing.T) {
	// A 403 rateLimitExceeded (observed on the production NAS alongside
	// the real page-token bug, issue #64 PR #79 review) must NOT be
	// treated as a token problem: it is neither 410 nor 400, so it falls
	// through to the plain-error branch unchanged.
	svc := newTestDriveService(t, func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprint(w, `{"error":{"errors":[{"domain":"usageLimits","reason":"rateLimitExceeded","message":"User Rate Limit Exceeded"}],"code":403,"message":"User Rate Limit Exceeded"}}`)
	})

	r := newTestRcloneFS(svc)
	r.rootID = "root-id"
	r.pathCache.put("root-id", "")

	_, err := r.pollChanges(context.Background(), "some-token")
	if err == nil {
		t.Fatal("expected an error")
	}
	if errors.Is(err, ErrGone) {
		t.Errorf("403 rateLimitExceeded must not wrap ErrGone, got: %v", err)
	}
}

func TestIsGoneErr_Cases(t *testing.T) {
	svc := newTestDriveService(t, func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusGone)
		_, _ = fmt.Fprint(w, `{"error":{"code":410,"message":"gone"}}`)
	})
	_, err := svc.Changes.List("tok").Do()
	if err == nil {
		t.Fatal("expected an error from the fake 410 server")
	}
	if !isGoneErr(err) {
		t.Errorf("isGoneErr(%v) = false, want true", err)
	}
	if isGoneErr(nil) {
		t.Errorf("isGoneErr(nil) = true, want false")
	}
}

func TestPollChanges_400BadRequest_ReturnsErrGone(t *testing.T) {
	// Body shaped exactly like the real production sample captured on the
	// NAS (issue #64 PR #79 review): a stub token ("synced", predating
	// even the initialSyncPrefix convention) fed into changes.list comes
	// back as a plain 400 whose message names no field at all --
	// Message == "Invalid Value", Errors[0].Reason == "invalid". Neither a
	// machine-readable field-violation nor a message-substring check
	// catches this (see isBadPageTokenErr's doc comment); pollChanges must
	// still recognize it as "reset the token" via the same ErrGone
	// sentinel, plus the additional ErrTokenRejected marker.
	svc := newTestDriveService(t, func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"error":{"errors":[{"domain":"global","reason":"invalid","message":"Invalid Value"}],"code":400,"message":"Invalid Value"}}`)
	})

	r := newTestRcloneFS(svc)
	r.rootID = "root-id"
	r.pathCache.put("root-id", "")

	_, err := r.pollChanges(context.Background(), "synced")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, ErrGone) {
		t.Errorf("expected error wrapping ErrGone, got: %v", err)
	}
	if !errors.Is(err, ErrTokenRejected) {
		t.Errorf("expected error wrapping ErrTokenRejected, got: %v", err)
	}
}

func TestIsBadPageTokenErr_Cases(t *testing.T) {
	cases := []struct {
		name       string
		statusCode int
		body       string
		want       bool
	}{
		{
			// The real production sample (issue #64 PR #79 review): no
			// field name anywhere in the message, yet this genuinely is
			// the stale-token case and must be matched.
			name:       "400 with generic message (real production sample) is matched",
			statusCode: http.StatusBadRequest,
			body:       `{"error":{"errors":[{"domain":"global","reason":"invalid","message":"Invalid Value"}],"code":400,"message":"Invalid Value"}}`,
			want:       true,
		},
		{
			name:       "400 naming pageToken parameter is matched",
			statusCode: http.StatusBadRequest,
			body:       `{"error":{"code":400,"message":"Invalid Value: pageToken"}}`,
			want:       true,
		},
		{
			// Any 400 on this call site is matched -- see isBadPageTokenErr's
			// doc comment for why "unrelated 400" isn't a real category here
			// (pageToken is the only runtime-variable input to this call).
			name:       "400 naming an unrelated field is still matched (blanket 400 on this call site)",
			statusCode: http.StatusBadRequest,
			body:       `{"error":{"code":400,"message":"Invalid Value: fields"}}`,
			want:       true,
		},
		{
			name:       "410 gone is not matched (handled by isGoneErr instead)",
			statusCode: http.StatusGone,
			body:       `{"error":{"code":410,"message":"Invalid page token"}}`,
			want:       false,
		},
		{
			// Observed on the same production host, interleaved with the
			// real 400 (issue #64 PR #79 review) -- must not be matched.
			name:       "403 rateLimitExceeded is not matched",
			statusCode: http.StatusForbidden,
			body:       `{"error":{"errors":[{"domain":"usageLimits","reason":"rateLimitExceeded","message":"User Rate Limit Exceeded"}],"code":403,"message":"User Rate Limit Exceeded"}}`,
			want:       false,
		},
		{
			name:       "200 ok has no error at all",
			statusCode: http.StatusOK,
			body:       `{"changes":[]}`,
			want:       false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := newTestDriveService(t, func(w http.ResponseWriter, req *http.Request) {
				w.WriteHeader(tc.statusCode)
				_, _ = fmt.Fprint(w, tc.body)
			})
			_, err := svc.Changes.List("tok").Do()

			got := isBadPageTokenErr(err)
			if got != tc.want {
				t.Errorf("isBadPageTokenErr(%v) = %v, want %v", err, got, tc.want)
			}
		})
	}

	if isBadPageTokenErr(nil) {
		t.Errorf("isBadPageTokenErr(nil) = true, want false")
	}

	// Non-googleapi errors (transport/OAuth failures never reach the
	// Drive HTTP layer as a *googleapi.Error) must not be matched either.
	nonGoogleErrs := []error{
		context.DeadlineExceeded,
		errors.New("dial tcp: connection refused"),
		fmt.Errorf("oauth2: cannot fetch token: %w", errors.New("invalid_client")),
	}
	for _, e := range nonGoogleErrs {
		if isBadPageTokenErr(e) {
			t.Errorf("isBadPageTokenErr(%v) = true, want false (not a googleapi.Error)", e)
		}
	}
}

// newOAuthFailingDriveService builds a *drivev3.Service whose HTTP client
// is wrapped in a real golang.org/x/oauth2 Transport pointed at a fake
// token endpoint (never Google's) that always returns the given RFC 6749
// token-endpoint error. The stored token is already expired, so the very
// first Drive API call triggers a silent refresh attempt against that
// fake endpoint and fails there -- the Drive endpoint itself is never
// reached, matching how the real "Could not determine client ID from
// request" failure occurs in production (issue #67). No real Google API
// call is made anywhere (per homedrive-test-mocks).
func newOAuthFailingDriveService(t *testing.T, tokenErrorCode, tokenErrorDescription string) *drivev3.Service {
	t.Helper()
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(t, w, map[string]string{
			"error":             tokenErrorCode,
			"error_description": tokenErrorDescription,
		})
	}))
	t.Cleanup(tokenSrv.Close)

	oauthCfg := &oauth2.Config{
		Endpoint: oauth2.Endpoint{TokenURL: tokenSrv.URL},
		// No ClientID/ClientSecret -- matches the production defect this
		// issue is about.
	}
	expiredTok := &oauth2.Token{
		AccessToken:  "stale-access-token",
		RefreshToken: "refresh-1",
		Expiry:       time.Now().Add(-time.Hour),
	}
	httpClient := oauthCfg.Client(context.Background(), expiredTok)

	svc, err := drivev3.NewService(context.Background(), option.WithHTTPClient(httpClient))
	if err != nil {
		t.Fatalf("build oauth-failing drive service: %v", err)
	}
	return svc
}

func TestPollChanges_OAuthClientMissing_ReturnsErrOAuthClientMissing(t *testing.T) {
	svc := newOAuthFailingDriveService(t, "invalid_request", "Could not determine client ID from request.")

	r := newTestRcloneFS(svc)
	r.rootID = "root-id"
	r.pathCache.put("root-id", "")
	// Simulates oauthHTTPClient having already observed the missing
	// client_id/client_secret precondition (see oauthHTTPClient in
	// driveapi.go) -- this test builds changesSvc directly and so
	// bypasses that call.
	r.oauthChecked = true
	r.oauthClientConfigured = false

	_, err := r.pollChanges(context.Background(), "tok")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, ErrOAuthClientMissing) {
		t.Errorf("expected error wrapping ErrOAuthClientMissing, got: %v", err)
	}
	// An OAuth refresh failure never reaches the Drive API response layer
	// isGoneErr/isBadPageTokenErr classify, so it must not be conflated
	// with the token-reset classes.
	if errors.Is(err, ErrGone) {
		t.Errorf("OAuth client-missing error must not wrap ErrGone, got: %v", err)
	}
}

func TestPollChanges_OAuthRetrieveError_WithClientConfigured_NotClassified(t *testing.T) {
	// Same failure shape (a *oauth2.RetrieveError reaching pollChanges),
	// but the client_id/client_secret precondition is NOT missing -- e.g.
	// a revoked or otherwise invalid refresh token (invalid_grant). Must
	// not be classified as ErrOAuthClientMissing: that would incorrectly
	// gate the puller's backoff on an unrelated condition (issue #67 is
	// explicit that backoff should be narrow, not "any auth failure").
	svc := newOAuthFailingDriveService(t, "invalid_grant", "Token has been expired or revoked.")

	r := newTestRcloneFS(svc)
	r.rootID = "root-id"
	r.pathCache.put("root-id", "")
	r.oauthChecked = true
	r.oauthClientConfigured = true

	_, err := r.pollChanges(context.Background(), "tok")
	if err == nil {
		t.Fatal("expected an error")
	}
	if errors.Is(err, ErrOAuthClientMissing) {
		t.Errorf("expected error NOT to wrap ErrOAuthClientMissing (client is configured), got: %v", err)
	}
}
