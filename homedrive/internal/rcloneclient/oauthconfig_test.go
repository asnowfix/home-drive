package rcloneclient

import (
	"encoding/json"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func TestBuildOAuthConfig_Cases(t *testing.T) {
	validToken, err := json.Marshal(oauth2.Token{
		AccessToken:  "access-1",
		RefreshToken: "refresh-1",
		TokenType:    "Bearer",
		Expiry:       time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("marshal test token: %v", err)
	}

	cases := []struct {
		name         string
		tokenJSON    string
		clientID     string
		clientSecret string
		wantErr      bool
		wantAccess   string
	}{
		{
			name:         "valid token with client credentials",
			tokenJSON:    string(validToken),
			clientID:     "id-1",
			clientSecret: "secret-1",
			wantAccess:   "access-1",
		},
		{
			name:       "valid token without client credentials still parses",
			tokenJSON:  string(validToken),
			wantAccess: "access-1",
		},
		{
			name:      "malformed token JSON",
			tokenJSON: "{not json",
			wantErr:   true,
		},
		{
			name:      "empty token JSON",
			tokenJSON: "",
			wantErr:   true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, tok, err := buildOAuthConfig(tc.tokenJSON, tc.clientID, tc.clientSecret)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("buildOAuthConfig: %v", err)
			}
			if tok.AccessToken != tc.wantAccess {
				t.Errorf("AccessToken = %q, want %q", tok.AccessToken, tc.wantAccess)
			}
			if cfg.ClientID != tc.clientID || cfg.ClientSecret != tc.clientSecret {
				t.Errorf("cfg = %+v, want ClientID=%q ClientSecret=%q", cfg, tc.clientID, tc.clientSecret)
			}
			if len(cfg.Scopes) != 1 {
				t.Errorf("Scopes = %v, want exactly one Drive scope", cfg.Scopes)
			}
		})
	}
}

func TestRemoteSectionName_Cases(t *testing.T) {
	cases := []struct{ in, want string }{
		{"gdrive:", "gdrive"},
		{"gdrive:subdir", "gdrive"},
		{"gdrive:sub/dir", "gdrive"},
		{"gdrive", "gdrive"},
	}
	for _, tc := range cases {
		if got := remoteSectionName(tc.in); got != tc.want {
			t.Errorf("remoteSectionName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
