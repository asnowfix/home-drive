package rcloneclient

import (
	"errors"

	"golang.org/x/oauth2"
)

// OAuthStatus reports the last-observed OAuth client-configuration
// precondition for a remote (see RcloneFS.OAuthStatus). It intentionally
// tracks only what oauthHTTPClient already determines as a side effect of
// building the Drive Changes API client -- never a live probe of its own,
// so reading it (e.g. from GET /healthz) never costs Drive API quota
// (issue #67; see the homedrive-rclone-import skill's "OAuth health"
// section, which this narrows to the single precondition this issue is
// actually about rather than a general expiry/refresh tracker).
type OAuthStatus struct {
	// Checked is false until the Changes API client has been built at
	// least once (normally within the first pull cycle after startup).
	// A false Checked means "not yet known", not "healthy".
	Checked bool

	// ClientConfigured is only meaningful when Checked is true: whether
	// rclone.conf had both client_id and client_secret set for this
	// remote the last time it was read. When false, the stored OAuth
	// token cannot be refreshed once it expires -- see
	// ErrOAuthClientMissing.
	ClientConfigured bool
}

// OAuthStatus returns the last-observed OAuth client-configuration state
// for this remote. Safe for concurrent use; never makes a network call.
func (r *RcloneFS) OAuthStatus() OAuthStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return OAuthStatus{Checked: r.oauthChecked, ClientConfigured: r.oauthClientConfigured}
}

// isOAuthClientMissingErr reports whether err is an OAuth token-refresh
// failure -- surfaced by golang.org/x/oauth2's Transport as a
// *oauth2.RetrieveError when it silently tries to refresh an expired
// access token -- that happened while this remote had no client_id /
// client_secret configured. Both conditions are required:
//
//   - clientConfigured must be false. A bare *oauth2.RetrieveError alone
//     is not enough: a revoked or invalid refresh token, even with
//     client credentials correctly configured, surfaces as the same Go
//     type for an unrelated reason (e.g. RFC 6749 "invalid_grant"), and
//     that is not the permanent, config-fixable condition issue #67 is
//     about.
//   - err must actually be a *oauth2.RetrieveError. The config
//     precondition alone is not enough either: it only describes what
//     rclone.conf looks like, not what actually failed -- a transport
//     error or a genuine Drive API error while the client happens to be
//     unconfigured must not be misclassified.
//
// This is deliberately narrower than "back off on any auth failure": see
// issue #67 for why over-generalising here was explicitly ruled out.
func isOAuthClientMissingErr(err error, clientConfigured bool) bool {
	if clientConfigured || err == nil {
		return false
	}
	var retrieveErr *oauth2.RetrieveError
	return errors.As(err, &retrieveErr)
}
