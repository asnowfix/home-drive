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

## Status: resumed after spend-limit kill, finishing

Picked up from commit 591895a (pushed by the maintainer after the previous
agent was killed by a hard spend limit mid-task). What was done in this
session:

1. Fixed the compile error: `oauthvalidate.go` was missing `"log/slog"`.
2. Wrote `oauthvalidate_test.go` (none existed before). Covers:
   - `forceOAuthRefresh` table-driven over all 4 client-setup RFC 6749 codes
     (-> `ErrOAuthClientMisconfigured`), `invalid_grant` + an unrecognized
     code (-> `ErrOAuthTokenInvalid`), and a successful refresh (-> nil).
   - Network-unreachable and context-timeout cases, both swallowed (nil).
   - `classifyOAuthRefreshErr` directly, table-driven, no network.
   - `validateOAuthCredential`'s three swallowed paths (no token stored,
     malformed token, no refresh token) using `config.FileSetValue` against
     unique section names -- same established pattern as
     `TestOAuthHTTPClient_RecordsClientConfiguredStatus` in
     `oauthstatus_test.go`. Deliberately does NOT test the "real refresh
     token, reaches forceOAuthRefresh" path through `validateOAuthCredential`
     end-to-end, because `readOAuthConfig` always builds `oauthCfg` against
     `google.Endpoint` -- there is no seam to swap in a fake token server at
     that layer, and reaching it would mean a real call to Google. That path
     is why `forceOAuthRefresh` was split out as a function of its inputs
     instead -- it's covered directly, with a fake server.
   - A secret-leak check (`assertNoSecretLeak`) asserting neither the
     returned error text nor captured log output contains the fake
     token/client-secret markers used in test fixtures.
   - Found and fixed a `t.Cleanup` LIFO-ordering deadlock in my own first
     draft of the timeout test: closing `blockUntil` was registered before
     `srv.Close()`, so `Close()` (which waits for in-flight handlers) hung
     forever waiting for a handler that could only return once `blockUntil`
     closed. Fixed by reordering registration.
   - Coverage: 84.0% for `internal/rcloneclient` (gate is 70%).
3. Resolved both open questions from the resume brief as documented
   decisions in `oauthvalidate.go`'s doc comment on
   `validateOAuthCredential` (not just flagged):
   - Non-persistence of the refreshed token: kept as-is, deliberately, with
     rationale (matches the pre-existing `oauthHTTPClient` gap; one call per
     startup, not per poll cycle; persisting risks a new hang if
     `rclone.conf` is encrypted and `Save()` wants a password with no TTY on
     a headless systemd service; proper fix is a homedrive-owned `Storage`,
     which is the deferred "own the secrets" option on #86).
   - Interaction with #88's restart policy: the brief's still-current
     concern was written against the *interim*, already-superseded
     never-give-up/flat-60s policy. #88 was corrected 2026-09-07 to "quick
     retries with exponential backoff, then fail after some attempts" --
     not yet implemented in code. Documented why that direction is strictly
     safer for this check (bounded attempts vs. unbounded), and flagged the
     one real open risk (an aggressively fast early-backoff floor could
     still burn several refreshes in the first second or two of a crash
     loop) as a #88 design question, not something this slice should
     second-guess.

## Before opening the PR

This file is for continuity only and must NOT land in the PR/main --
delete it in the last local commit before pushing, per the resume brief.
