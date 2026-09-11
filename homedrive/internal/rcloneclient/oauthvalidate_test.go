package rcloneclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rclone/rclone/fs/config"
	"golang.org/x/oauth2"
)

// fakeTokenSecret and fakeClientSecret are markers embedded in test fixtures
// standing in for real credential material. They are asserted to never
// appear in any log line or returned error string (per this package's
// absolute "never log a secret" rule) -- not because the fake token
// endpoints in this file would ever echo them back (they don't), but as a
// structural check that no current or future code path in oauthvalidate.go
// starts interpolating token/secret values into a log or error message.
const (
	fakeTokenSecret  = "test-refresh-token-do-not-log-me"
	fakeClientSecret = "test-client-secret-do-not-log-me"
)

// newFakeTokenServer builds an httptest.Server that always answers a token
// request with the given status and JSON body, standing in for Google's
// real token endpoint (per homedrive-test-mocks: no real Google API calls
// in tests). oauth2.Config.Endpoint.TokenURL is pointed at it instead of
// google.Endpoint.
func newFakeTokenServer(t *testing.T, status int, body map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if err := json.NewEncoder(w).Encode(body); err != nil {
			t.Fatalf("encode fake token response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// testOAuthConfig builds an oauth2.Config pointed at tokenURL rather than
// google.Endpoint -- the seam forceOAuthRefresh's doc comment describes as
// what makes it unit-testable without ever reaching Google.
func testOAuthConfig(tokenURL string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     "test-client-id",
		ClientSecret: fakeClientSecret,
		Endpoint:     oauth2.Endpoint{TokenURL: tokenURL},
	}
}

func testOAuthToken() *oauth2.Token {
	return &oauth2.Token{
		AccessToken:  "test-stale-access-token",
		RefreshToken: fakeTokenSecret,
		Expiry:       time.Now().Add(time.Hour), // forceOAuthRefresh forces this stale itself
	}
}

// bufferLogger returns a JSON slog.Logger writing to a buffer the test can
// inspect afterwards, so secret-leak assertions can check actual log output
// rather than only the returned error's Error() string.
func bufferLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewJSONHandler(&buf, nil)), &buf
}

func TestForceOAuthRefresh_Cases(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       map[string]any
		wantErrIs  error // nil means "no error"
		wantErrNil bool
	}{
		{
			name:   "successful refresh returns nil",
			status: http.StatusOK,
			body: map[string]any{
				"access_token": "fresh-access-token",
				"token_type":   "Bearer",
				"expires_in":   3600,
			},
			wantErrNil: true,
		},
		{
			name:      "invalid_client is classified as client misconfigured",
			status:    http.StatusBadRequest,
			body:      map[string]any{"error": "invalid_client", "error_description": "The OAuth client was not found."},
			wantErrIs: ErrOAuthClientMisconfigured,
		},
		{
			name:      "unauthorized_client is classified as client misconfigured",
			status:    http.StatusBadRequest,
			body:      map[string]any{"error": "unauthorized_client", "error_description": "Unauthorized."},
			wantErrIs: ErrOAuthClientMisconfigured,
		},
		{
			name:      "unsupported_grant_type is classified as client misconfigured",
			status:    http.StatusBadRequest,
			body:      map[string]any{"error": "unsupported_grant_type", "error_description": "Unsupported grant."},
			wantErrIs: ErrOAuthClientMisconfigured,
		},
		{
			name:      "invalid_scope is classified as client misconfigured",
			status:    http.StatusBadRequest,
			body:      map[string]any{"error": "invalid_scope", "error_description": "Bad scope."},
			wantErrIs: ErrOAuthClientMisconfigured,
		},
		{
			name:      "invalid_grant is classified as token invalid",
			status:    http.StatusBadRequest,
			body:      map[string]any{"error": "invalid_grant", "error_description": "Token has been expired or revoked."},
			wantErrIs: ErrOAuthTokenInvalid,
		},
		{
			name:      "an error code outside rclone's client-setup bucket falls into token invalid",
			status:    http.StatusBadRequest,
			body:      map[string]any{"error": "server_error", "error_description": "Something else went wrong."},
			wantErrIs: ErrOAuthTokenInvalid,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newFakeTokenServer(t, tc.status, tc.body)
			log, logBuf := bufferLogger()

			err := forceOAuthRefresh(context.Background(), testOAuthConfig(srv.URL), testOAuthToken(), "test-remote", log)

			if tc.wantErrNil {
				if err != nil {
					t.Fatalf("forceOAuthRefresh: got %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("forceOAuthRefresh: got nil, want a classified error")
			}
			if !errors.Is(err, tc.wantErrIs) {
				t.Errorf("forceOAuthRefresh error = %v, want errors.Is(_, %v)", err, tc.wantErrIs)
			}
			if !strings.Contains(err.Error(), oauthTroubleshootingDoc) {
				t.Errorf("error %q does not cite %q", err.Error(), oauthTroubleshootingDoc)
			}
			if !strings.Contains(err.Error(), "test-remote") {
				t.Errorf("error %q does not name the remote", err.Error())
			}

			assertNoSecretLeak(t, err.Error(), logBuf.String())
		})
	}
}

// assertNoSecretLeak fails the test if either the returned error text or the
// captured log output contains this package's fake secret markers. See the
// fakeTokenSecret/fakeClientSecret doc comment for why this is checked
// structurally rather than assumed.
func assertNoSecretLeak(t *testing.T, errText, logText string) {
	t.Helper()
	for _, secret := range []string{fakeTokenSecret, fakeClientSecret} {
		if strings.Contains(errText, secret) {
			t.Errorf("returned error leaks a secret value: %q", errText)
		}
		if strings.Contains(logText, secret) {
			t.Errorf("log output leaks a secret value: %q", logText)
		}
	}
}

// TestForceOAuthRefresh_NetworkUnreachable_ReturnsNilSwallowed proves a
// transport-level failure (the token endpoint cannot be reached at all) is
// swallowed rather than failing startup -- a boot before the network is up
// must not brick the agent (see forceOAuthRefresh's doc comment).
func TestForceOAuthRefresh_NetworkUnreachable_ReturnsNilSwallowed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	unreachableURL := srv.URL
	srv.Close() // closed before use: connections to it now fail immediately

	log, _ := bufferLogger()
	err := forceOAuthRefresh(context.Background(), testOAuthConfig(unreachableURL), testOAuthToken(), "test-remote", log)
	if err != nil {
		t.Errorf("forceOAuthRefresh with unreachable token endpoint = %v, want nil (swallowed)", err)
	}
}

// TestForceOAuthRefresh_Timeout_ReturnsNilSwallowed proves a context
// deadline exceeded while waiting for the token endpoint is swallowed the
// same way a network failure is: it is not a definitive answer from
// Google, so it must not be treated as fatal.
func TestForceOAuthRefresh_Timeout_ReturnsNilSwallowed(t *testing.T) {
	blockUntil := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-blockUntil // never respond within the test's short deadline
		w.WriteHeader(http.StatusOK)
	}))
	// Cleanup order matters here: t.Cleanup runs LIFO, and
	// httptest.Server.Close() blocks until every in-flight handler
	// returns -- so blockUntil must be closed (unblocking the handler)
	// before Close() runs, meaning its cleanup must be registered AFTER
	// srv's. Registering it the other way around deadlocks the test.
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(blockUntil) })

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	log, _ := bufferLogger()
	err := forceOAuthRefresh(ctx, testOAuthConfig(srv.URL), testOAuthToken(), "test-remote", log)
	if err != nil {
		t.Errorf("forceOAuthRefresh on timeout = %v, want nil (swallowed)", err)
	}
}

func TestClassifyOAuthRefreshErr_Cases(t *testing.T) {
	cases := []struct {
		errorCode string
		want      error
	}{
		{"invalid_client", ErrOAuthClientMisconfigured},
		{"unauthorized_client", ErrOAuthClientMisconfigured},
		{"unsupported_grant_type", ErrOAuthClientMisconfigured},
		{"invalid_scope", ErrOAuthClientMisconfigured},
		{"invalid_grant", ErrOAuthTokenInvalid},
		{"access_denied", ErrOAuthTokenInvalid},
		{"", ErrOAuthTokenInvalid},
	}

	for _, tc := range cases {
		t.Run(tc.errorCode, func(t *testing.T) {
			retrieveErr := &oauth2.RetrieveError{ErrorCode: tc.errorCode, ErrorDescription: "irrelevant"}
			err := classifyOAuthRefreshErr("test-remote", retrieveErr)
			if !errors.Is(err, tc.want) {
				t.Errorf("classifyOAuthRefreshErr(%q) = %v, want errors.Is(_, %v)", tc.errorCode, err, tc.want)
			}
			if !errors.Is(err, retrieveErr) {
				t.Errorf("classifyOAuthRefreshErr(%q) = %v, want it to wrap the original retrieveErr", tc.errorCode, err)
			}
		})
	}
}

// TestValidateOAuthCredential_NoTokenStored_SkipsAndReturnsNil covers a
// remote with nothing in rclone.conf yet -- readOAuthConfig fails, and
// validateOAuthCredential must not treat that as fatal (every real Drive
// API call will fail loudly and specifically once the remote is actually
// used; that is not this check's job to diagnose). Checked must stay false:
// "not yet known", per OAuthStatus's documented semantics.
func TestValidateOAuthCredential_NoTokenStored_SkipsAndReturnsNil(t *testing.T) {
	const section = "test-oauthvalidate-no-token"
	r := &RcloneFS{remoteName: section, log: slog.Default()}

	if err := r.validateOAuthCredential(context.Background()); err != nil {
		t.Fatalf("validateOAuthCredential = %v, want nil", err)
	}
	if got := r.OAuthStatus(); got.Checked {
		t.Errorf("OAuthStatus() = %+v, want Checked=false (never reached readOAuthConfig's success path)", got)
	}
}

// TestValidateOAuthCredential_MalformedToken_SkipsAndReturnsNil covers a
// stored token that isn't valid JSON -- readOAuthConfig's buildOAuthConfig
// call fails, and this must be swallowed the same way a missing token is.
func TestValidateOAuthCredential_MalformedToken_SkipsAndReturnsNil(t *testing.T) {
	const section = "test-oauthvalidate-malformed-token"
	t.Cleanup(func() { config.FileDeleteKey(section, "token") })
	config.FileSetValue(section, "token", "{not json")

	r := &RcloneFS{remoteName: section, log: slog.Default()}

	if err := r.validateOAuthCredential(context.Background()); err != nil {
		t.Fatalf("validateOAuthCredential = %v, want nil", err)
	}
	if got := r.OAuthStatus(); got.Checked {
		t.Errorf("OAuthStatus() = %+v, want Checked=false", got)
	}
}

// TestValidateOAuthCredential_NoRefreshToken_RecordsCheckedButSkipsRefresh
// covers a remote with a token stored but no refresh_token in it. This is
// the one case that exercises readOAuthConfig's success path (so
// oauthChecked/oauthClientConfigured get recorded, exactly as they would
// from the first real Changes API call) while still never reaching
// forceOAuthRefresh -- so this test, like the others in this file, never
// calls out over the network: oauthCfg here always resolves to
// google.Endpoint (readOAuthConfig -> buildOAuthConfig hardcodes it), which
// is exactly why the no-refresh-token guard is what is exercised, not an
// actual refresh (per homedrive-test-mocks: no real Google API calls, ever).
func TestValidateOAuthCredential_NoRefreshToken_RecordsCheckedButSkipsRefresh(t *testing.T) {
	const section = "test-oauthvalidate-no-refresh-token"
	t.Cleanup(func() {
		config.FileDeleteKey(section, "token")
		config.FileDeleteKey(section, "client_id")
		config.FileDeleteKey(section, "client_secret")
	})

	tokenJSON, err := json.Marshal(oauth2.Token{
		AccessToken: "access-only-no-refresh",
		TokenType:   "Bearer",
		Expiry:      time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("marshal test token: %v", err)
	}
	config.FileSetValue(section, "token", string(tokenJSON))
	config.FileSetValue(section, "client_id", "id-1")
	config.FileSetValue(section, "client_secret", "secret-1")

	r := &RcloneFS{remoteName: section, log: slog.Default()}

	if err := r.validateOAuthCredential(context.Background()); err != nil {
		t.Fatalf("validateOAuthCredential = %v, want nil", err)
	}
	if got := r.OAuthStatus(); !got.Checked || !got.ClientConfigured {
		t.Errorf("OAuthStatus() = %+v, want Checked=true ClientConfigured=true", got)
	}
}
