package rcloneclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/rclone/rclone/fs/config"
	"golang.org/x/oauth2"
)

func TestIsOAuthClientMissingErr_Cases(t *testing.T) {
	retrieveErr := &oauth2.RetrieveError{
		ErrorCode:        "invalid_request",
		ErrorDescription: "Could not determine client ID from request.",
	}

	cases := []struct {
		name             string
		err              error
		clientConfigured bool
		want             bool
	}{
		{
			name:             "missing client id with a real oauth2 retrieve error is classified",
			err:              retrieveErr,
			clientConfigured: false,
			want:             true,
		},
		{
			name:             "wrapped retrieve error is still detected via errors.As",
			err:              fmt.Errorf("get token: %w", retrieveErr),
			clientConfigured: false,
			want:             true,
		},
		{
			name: "same error type, but client IS configured, is not classified " +
				"(e.g. a revoked refresh token returns the same Go type for an " +
				"unrelated, non-config reason)",
			err:              retrieveErr,
			clientConfigured: true,
			want:             false,
		},
		{
			name:             "missing client id but an unrelated error type is not classified",
			err:              errors.New("network unreachable"),
			clientConfigured: false,
			want:             false,
		},
		{
			name:             "nil error is never classified",
			err:              nil,
			clientConfigured: false,
			want:             false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isOAuthClientMissingErr(tc.err, tc.clientConfigured); got != tc.want {
				t.Errorf("isOAuthClientMissingErr(%v, %v) = %v, want %v", tc.err, tc.clientConfigured, got, tc.want)
			}
		})
	}
}

func TestRcloneFS_OAuthStatus_Cases(t *testing.T) {
	r := &RcloneFS{}
	if got := r.OAuthStatus(); got.Checked || got.ClientConfigured {
		t.Errorf("zero-value RcloneFS.OAuthStatus() = %+v, want both false (not yet checked)", got)
	}

	r.oauthChecked = true
	r.oauthClientConfigured = false
	if got := r.OAuthStatus(); !got.Checked || got.ClientConfigured {
		t.Errorf("OAuthStatus() = %+v, want Checked=true ClientConfigured=false", got)
	}

	r.oauthClientConfigured = true
	if got := r.OAuthStatus(); !got.Checked || !got.ClientConfigured {
		t.Errorf("OAuthStatus() = %+v, want Checked=true ClientConfigured=true", got)
	}
}

// TestOAuthHTTPClient_RecordsClientConfiguredStatus proves oauthHTTPClient
// (the only writer of oauthChecked/oauthClientConfigured) observes the
// rclone.conf client_id/client_secret precondition correctly, using
// config.FileSetValue against a uniquely-named section so it never touches
// a real remote (per homedrive-test-mocks: no real Google API calls in
// tests -- this never dials out, it only reads rclone's in-memory config
// store).
func TestOAuthHTTPClient_RecordsClientConfiguredStatus(t *testing.T) {
	const section = "test-oauthstatus-remote"
	t.Cleanup(func() {
		config.FileDeleteKey(section, "token")
		config.FileDeleteKey(section, "client_id")
		config.FileDeleteKey(section, "client_secret")
	})

	tokenJSON, err := json.Marshal(oauth2.Token{
		AccessToken:  "access-1",
		RefreshToken: "refresh-1",
		TokenType:    "Bearer",
		Expiry:       time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("marshal test token: %v", err)
	}
	config.FileSetValue(section, "token", string(tokenJSON))

	r := &RcloneFS{remoteName: section, log: slog.Default()}

	if _, err := r.oauthHTTPClient(context.Background()); err != nil {
		t.Fatalf("oauthHTTPClient (no client_id/secret): %v", err)
	}
	if got := r.OAuthStatus(); !got.Checked || got.ClientConfigured {
		t.Errorf("OAuthStatus() = %+v, want Checked=true ClientConfigured=false", got)
	}

	config.FileSetValue(section, "client_id", "id-1")
	config.FileSetValue(section, "client_secret", "secret-1")

	if _, err := r.oauthHTTPClient(context.Background()); err != nil {
		t.Fatalf("oauthHTTPClient (with client_id/secret): %v", err)
	}
	if got := r.OAuthStatus(); !got.Checked || !got.ClientConfigured {
		t.Errorf("OAuthStatus() = %+v, want Checked=true ClientConfigured=true", got)
	}
}
