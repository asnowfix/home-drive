package rcloneclient

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"golang.org/x/oauth2"
)

// oauthValidationTimeout bounds the one forced token-refresh call
// validateOAuthCredential makes at startup, so a network hiccup at boot
// cannot hang agent startup indefinitely -- the pull loop's existing
// retry/backoff already covers ongoing connectivity issues once the agent
// is up.
const oauthValidationTimeout = 20 * time.Second

// oauthTroubleshootingDoc is cited, not restated, in every classified
// startup OAuth validation failure. It has the documented causes, a Cloud
// Console checklist and the headless re-auth procedure (issue #86 scope
// decision).
const oauthTroubleshootingDoc = "see homedrive/docs/oauth-troubleshooting.md"

// clientSetupErrorCodes are the RFC 6749 'error' values that rclone's own
// lib/oauthutil/oauthutil.go (v1.75.0, maybeWrapOAuthError) buckets as
// "client id/secret setup is wrong", as opposed to "the token itself is
// stale/revoked" -- see homedrive/docs/oauth-troubleshooting.md Q1, which
// cites that source directly. invalid_grant, and any code not in this set,
// falls into the other bucket (ErrOAuthTokenInvalid).
var clientSetupErrorCodes = map[string]bool{
	"invalid_client":         true,
	"unauthorized_client":    true,
	"unsupported_grant_type": true,
	"invalid_scope":          true,
}

// validateOAuthCredential forces a refresh of remoteName's stored OAuth
// token against Google's real token endpoint, once, at startup -- so a
// broken or mismatched Drive credential (issue #87: a token minted by one
// OAuth client while a different client's client_id/client_secret sat in
// rclone.conf) is reported immediately instead of appearing healthy for up
// to an hour, until the cached access token's natural expiry (issue #86).
// An access token is an opaque bearer credential, so simply having one that
// still works proves nothing about whether it can ever be refreshed again.
//
// It intentionally cannot detect a client_id/token mismatch by comparing
// fields in rclone.conf: the stored token blob does not record which
// client_id minted it (issue #86 discussion), so no static check can catch
// it -- only an actual refresh attempt, which is what this does, can.
//
// Only a definitive rejection from the token endpoint itself
// (*oauth2.RetrieveError, meaning Google's server actually answered) is
// treated as fatal and returned to the caller (NewRcloneFS, which then
// fails agent startup loudly instead of starting up degraded). Anything
// else -- no refresh token stored yet, network unreachable, hitting
// oauthValidationTimeout -- is logged and swallowed, matching
// OAuthStatus.Checked's existing "not yet known" semantics: those are not
// the permanent, config-fixable condition this check exists to catch, and
// turning a transient boot-time network hiccup into a hard startup failure
// would be worse than issue #87's original "silent for a while" defect,
// deliberately narrower than "fail on any oauth error" for the same reason
// isOAuthClientMissingErr (oauthstatus.go) is narrow.
//
// Decision: the refreshed token is deliberately never persisted back to
// rclone.conf, so every startup burns one refresh call. Considered and
// rejected persisting it: oauthHTTPClient's own refreshes already have this
// same gap (not a regression introduced here), the cost is one call per
// process startup -- not per poll cycle -- so it is negligible in steady
// state, and writing through configfile's Storage from a headless
// systemd service risks a new failure mode this check exists to prevent
// (e.g. an encrypted rclone.conf's Save() path prompting for a config
// password with no TTY to answer it, turning a benign non-persistence gap
// into a hang). Persistence is better addressed by a homedrive-owned
// Storage, which is exactly the deferred "own the secrets" option on issue
// #86 -- not this validate-only slice.
//
// Decision: this forces a refresh on every process startup, including
// every restart of a crash-looping agent. At the time of writing, issue #88
// is *designed* (not yet implemented) to replace the currently-deployed
// never-give-up/flat-60s restart policy with a bounded number of attempts
// on a quick exponential backoff. That direction is strictly safer for this
// check than the interim policy actually running in production: bounded
// attempts cap the total refresh calls a dead credential can trigger,
// where the interim flat-60s policy does not. The one open risk is #88's
// still-undecided backoff floor -- if early retries land near-instantly,
// this check could burn several refreshes within the first second or two
// of a crash loop. That is a #88 design question (what "quick" and "some
// attempts" mean), not something this slice should second-guess by, say,
// adding its own rate limiting -- see this PR's description for why.
func (r *RcloneFS) validateOAuthCredential(ctx context.Context) error {
	oauthCfg, tok, clientConfigured, err := readOAuthConfig(r.remoteName)
	if err != nil {
		// No token stored yet, or unparsable -- not this check's job to
		// diagnose; every real Drive API call will fail loudly and
		// specifically once the remote is actually used.
		r.log.Warn("startup oauth validation: skipped", "remote", r.remoteName, "error", err)
		return nil
	}

	// Populate the same cache oauthHTTPClient does, so GET /healthz
	// reflects the client_id/client_secret precondition from startup
	// rather than only after the first pull cycle reaches driveService.
	r.oauthChecked = true
	r.oauthClientConfigured = clientConfigured

	if tok.RefreshToken == "" {
		r.log.Warn("startup oauth validation: skipped, no refresh token stored for remote",
			"remote", r.remoteName)
		return nil
	}

	return forceOAuthRefresh(ctx, oauthCfg, tok, r.remoteName, r.log)
}

// forceOAuthRefresh does the actual forced-refresh network call and
// classification described on validateOAuthCredential's doc comment. Split
// out as a function of its inputs (rather than an RcloneFS method) so it is
// unit-testable against a fake token endpoint without going through
// rclone.conf at all -- oauthCfg.Endpoint.TokenURL is swapped for a
// httptest server in tests (see oauthvalidate_test.go); production always
// reaches this with oauthCfg built by buildOAuthConfig, which always uses
// google.Endpoint.
func forceOAuthRefresh(ctx context.Context, oauthCfg *oauth2.Config, tok *oauth2.Token, remoteName string, log *slog.Logger) error {
	vctx, cancel := context.WithTimeout(ctx, oauthValidationTimeout)
	defer cancel()

	// A copy with a forced-past expiry: oauth2's TokenSource only calls
	// the token endpoint when it considers the current token invalid, and
	// the cached access token this same remote is otherwise about to use
	// is -- by definition of the bug this exists to catch -- still valid
	// as far as the TokenSource can tell. This is the forced part of
	// "forced refresh".
	forced := *tok
	forced.Expiry = time.Unix(0, 0)

	if _, refreshErr := oauthCfg.TokenSource(vctx, &forced).Token(); refreshErr != nil {
		var retrieveErr *oauth2.RetrieveError
		if !errors.As(refreshErr, &retrieveErr) {
			log.Warn("startup oauth validation: could not reach token endpoint; "+
				"deferring to the normal pull-loop retry/backoff",
				"remote", remoteName, "error", refreshErr)
			return nil
		}
		return classifyOAuthRefreshErr(remoteName, retrieveErr)
	}

	log.Info("startup oauth validation: token refresh succeeded", "remote", remoteName)
	return nil
}

// classifyOAuthRefreshErr turns a definitive rejection from Google's OAuth
// token endpoint into one of two distinct, actionable sentinel errors,
// matching rclone's own bucketing of the same RFC 6749 error codes (see
// clientSetupErrorCodes) instead of homedrive re-deriving or restating it.
// The classified error's text names the specific error code and points at
// homedrive/docs/oauth-troubleshooting.md rather than duplicating its
// content.
func classifyOAuthRefreshErr(remoteName string, retrieveErr *oauth2.RetrieveError) error {
	if clientSetupErrorCodes[retrieveErr.ErrorCode] {
		return fmt.Errorf(
			"%w: remote %q's client_id/client_secret was rejected by Google's token endpoint (%s) -- %s: %w",
			ErrOAuthClientMisconfigured, remoteName, retrieveErr.ErrorCode, oauthTroubleshootingDoc, retrieveErr,
		)
	}
	return fmt.Errorf(
		"%w: remote %q's stored refresh token was rejected by Google's token endpoint (%s) -- %s: %w",
		ErrOAuthTokenInvalid, remoteName, retrieveErr.ErrorCode, oauthTroubleshootingDoc, retrieveErr,
	)
}
