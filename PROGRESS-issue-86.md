# Issue #86 scoped slice: validate rclone.conf at startup

Scope (per "Scope decision 2026-09-07" comment on issue #86): ONLY startup
validation of the OAuth credential via a forced token refresh, classified
into two actionable buckets, citing (not restating) oauth-troubleshooting.md.
Explicitly NOT: custom Storage, owned secrets, encrypted mounts, wizard
removal, migration tooling, touching fs/config/configfile.

## Plan
1. Refactor driveapi.go: extract `readOAuthConfig(remoteName)` (pure) out of
   `oauthHTTPClient`, so both the live Changes API path and the new startup
   validation share one place that reads token/client_id/client_secret from
   rclone.conf.
2. New file `internal/rcloneclient/oauthvalidate.go`:
   - `RcloneFS.validateOAuthCredential(ctx)`: forces a refresh (expired copy
     of the stored token) against the real oauth2.Config token endpoint,
     bounded by a 20s timeout. Only a definitive `*oauth2.RetrieveError` is
     treated as fatal; network/timeout/no-refresh-token is logged and
     swallowed (existing pull-loop retry/backoff already covers transient
     connectivity).
   - `classifyOAuthRefreshErr`: buckets RFC 6749 error codes the same way
     rclone's own lib/oauthutil does (see oauth-troubleshooting.md Q1) into
     ErrOAuthClientMisconfigured vs ErrOAuthTokenInvalid, citing the doc.
3. New sentinels in errors.go: ErrOAuthClientMisconfigured, ErrOAuthTokenInvalid.
4. Wire into `NewRcloneFS` (rclonefs.go) right after building the RcloneFS
   struct -- this is the traps-noted real startup path
   (cmd/homedrive/agent.go:124 -> rcloneclient.NewRcloneFS).
5. Tests: reuse the `newOAuthFailingDriveService`-style httptest fake token
   endpoint pattern already in driveapi_test.go (never real Google calls).
6. Build pipeline: make test (macOS), then orb run -m dev -- go test -race
   ./homedrive/... , then binary size + rclone import checks.

## Decisions / non-mandates taken up
- Took the maintainer's "force a refresh at startup" suggestion as-is: it's
  the only mechanism that can distinguish a broken client from a merely
  not-yet-expired one, given the token blob doesn't record its minting
  client_id (confirmed impossible to check statically, per the issue).
- Did NOT persist the refreshed token back to rclone.conf: this validation
  step discards the fresh token after checking it worked. Persisting would
  require writing through configfile's Storage, arguably in scope, but the
  existing driveapi.go oauthHTTPClient path already has this same
  non-persistence gap so it is not a regression, and doing so was not asked
  for by the scope decision. Flagging as a known limitation, out of scope.
- Only a *oauth2.RetrieveError (a definitive answer from Google's token
  endpoint) fails startup hard. Anything else (network down at boot, no
  refresh token yet stored) is logged, not fatal -- narrower than "fail on
  any oauth error", consistent with isOAuthClientMissingErr's existing
  "deliberately narrower" precedent in this codebase.

## Status: implementing
