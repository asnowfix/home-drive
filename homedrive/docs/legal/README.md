# Legal documents and Google OAuth branding: research notes

## What was asked

What Google requires for an External OAuth app that requests the restricted
scope `https://www.googleapis.com/auth/drive` to move from "Testing" to
"In production" for personal or small-scale use: whether verification is
needed, whether the homepage, privacy policy and terms links must sit on a
verified domain, whether a `github.com` URL is acceptable, and what the
Limited Use disclosure requires. Plus the two documents written from that
research: `privacy-policy.md` and `terms-of-service.md` in this directory.

## Short answers

1. **Verification.** Google's personal-use exemption covers an app used "only
   by a few users" who are "known personally" to the developer. homedrive is
   public open-source software, so the exemption does not describe its general
   distribution. Without verification, an unverified app that requests a
   restricted scope shows the "unverified app" screen and is limited to
   100 new users in total. Google's docs describe verification as required
   "before you launch a user-facing app".
2. **Domains.** Yes. Google requires verification of the domains used for the
   homepage, privacy policy and authorized domains. The homepage must be on a
   verified domain you own. The privacy policy must be on the same domain as
   the homepage.
3. **github.com.** The docs do not name GitHub. Under the stated rule
   (a verified domain you own), `https://github.com/asnowfix/home-drive` does
   not qualify: the maintainer cannot verify `github.com`. A `github.io`
   subdomain is a different case, see the findings below.
4. **Limited Use.** Applies because the app requests a restricted scope. The
   privacy policy must disclose the data practices and comply with the Limited
   Use requirements. Google provides a sample public statement, quoted below.

## Findings

Dates: pages were fetched on **2026-11-15**. Where a page shows a date, it is
given. Most Google Cloud help pages show only "©2026 Google" in the footer, with
no last-updated date.

### Verification and the personal-use exemption

- Restricted-scope verification page
  (<https://developers.google.com/identity/protocols/oauth2/production-readiness/restricted-scope-verification>,
  fetched 2026-11-15, no date shown):
  - Under "Exceptions to verification requirements" > "Personal use": "One use
    case is if you are the only user of your app or if your app is used by only
    a few users", and these users must be "known personally" to you.
  - "If your app requests any restricted scopes and doesn't qualify for" an
    exception, "you must meet the additional requirements, which can involve a
    more extensive review."
- Unverified apps page
  (<https://support.google.com/cloud/answer/7454865>, fetched 2026-11-15):
  - "An unverified app is an app or Apps Script that requests a sensitive or
    restricted OAuth scope"
  - "The app or script might display an 'unverified app' screen before it
    displays the consent screen."
  - Quota table: "100 new users in total, after the app presents the unverified
    app screen." Also "your app will be limited to 100 new users until it is
    verified."
  - "You need to go through verification before you launch a user-facing app."
    Development and test builds are exempt "unless you decide to launch it to the
    public."
- Verification requirements page
  (<https://support.google.com/cloud/answer/13464321>, fetched 2026-11-15) says
  certain scenarios are exempt, and points to a separate "When is verification
  not needed" section. That section was not readable in the fetch.

### Security assessment (CASA)

- The restricted-scope page says: "If you store or transmit restricted scope
  data on servers, then you need to complete a security assessment." The
  assessment condition is worded differently in different sections ("has the
  ability to access data from or through a third-party server", "accesses or has
  the capability to access Google user data from or through a server").
- Whether homedrive triggers this is **not established**. homedrive runs on the
  user's machine and sends data straight to Google. The maintainer runs no server
  that touches the data. Reading that as "no assessment needed" is an inference,
  and Google's wording is broad enough that it should be confirmed with Google
  before relying on it.

### Homepage, privacy policy, terms of service

- Brand verification page
  (<https://developers.google.com/identity/protocols/oauth2/production-readiness/brand-verification>,
  fetched 2026-11-15, no date shown):
  - Homepage: "Your home page must be publicly accessible, and not just
    accessible to your site's logged-in users." "The relevance of your home page
    to the app that's under review must be clear." Play Store or Facebook listing
    links "aren't considered valid application home pages."
  - "the home page must include a description of the app's functionality, as well
    as links to the privacy policy and optional terms of service."
  - Privacy policy: "The privacy policy must be visible to users, hosted within
    the same domain as your application's home page" and "linked to on the OAuth
    consent screen of the Google API Console." It "must disclose the manner in
    which your application accesses, uses, stores, or shares Google user data."
  - **Terms of service are optional.** The page calls them "optional terms of
    service". The brief for this work assumed a ToS link was required. The
    Google page does not support that.
  - Authorized domains: "Google requires verification of all domains that are
    associated with an application's OAuth consent screen and credentials." "We
    ask you to verify the domain component available for registration on a public
    suffix: the 'top private domain.'" Ownership is verified "using the Google
    Search Console".
- Verification requirements page
  (<https://support.google.com/cloud/answer/13464321>, fetched 2026-11-15):
  - "The homepage must be hosted on a verified domain you own."
  - "Your homepage can not be only a login page."
  - "The Privacy Policy should be hosted within the domain that hosts your
    homepage."
  - "You must verify that you own all domains listed in your Authorized domains
    section."
- Client verification help page
  (<https://support.google.com/cloud/answer/10311615>, fetched 2026-11-15):
  - "If you have verified the domain with Google, you can use any Top Private
    Domain as an Authorized Domain."
  - "After you add an Authorized Domain, you can use any of its subdomains or
    pages, and any other associated country codes."
  - "All domains used in your project, whether in the branding page or client
    configuration pages must be pre-registered here."
  - Branding: "In order for your app name and/or logo to be displayed, you must
    submit your app for verification." Without verification "only your application
    domain will be visible to users."
- Public Suffix List (<https://publicsuffix.org/list/public_suffix_list.dat>,
  file header `VERSION: 2026-11-14_13-54-10_UTC`, fetched 2026-11-15):
  - `github.io` is listed under the PRIVATE DOMAINS section. So
    `asnowfix.github.io` is itself a top private domain.
  - `github.com` is not listed as a suffix. Its top private domain is
    `github.com`, which the maintainer does not own.
  - Consequence (inference from the pages above, not stated by Google): a
    `github.io` subdomain *may* be verifiable as an authorized domain through
    Search Console URL-prefix verification, which needs a verification file or
    meta tag on the site. Whether Google's Cloud Console accepts a
    `*.github.io` top private domain was **not established**. A search summary
    claimed it probably cannot be verified. That claim is not from a Google page
    and the search summary did not cite one.
  - No GitHub Pages site appears to be configured for this repository. A
    `gh api` call to `repos/asnowfix/home-drive/pages` returned 404 on
    2026-11-15 (this indicates no Pages site, or no visibility of one).

### Limited Use and privacy policy content

- User Data Policy
  (<https://developers.google.com/terms/api-services-user-data-policy>, page shows
  "Last updated February 15, 2024"):
  - "You must publish a privacy policy that fully documents how your application
    interacts with user data."
  - Limited Use section: "Your use of data obtained via the product's specified
    scopes must comply with the below requirements." Requirements include: limit
    use "to providing or improving user-facing features"; "Transfers of data are
    not allowed, except:"; "Don't allow humans to read the data, unless:"; and "All
    other transfers, uses, or sales of user data are prohibited, including:
    Transferring or selling user data to third parties like advertising platforms,
    data brokers, or any information resellers."
- Restricted-scope page, "Application privacy policy link requirements": the
  privacy policy must comply with "the Limited Use requirements for restricted
  scopes", and use of Google user data "must be limited to the practices that
  your published privacy policy discloses". That page does not define the
  Limited Use requirements. It refers to the User Data Policy above.
- Sample public statement (Google Cloud help,
  <https://support.google.com/cloud/answer/13463817>, fetched 2026-11-15, read
  through a summarizing fetch): "{App's} use of information received from Google
  APIs will adhere to Google API Services User Data Policy, including the Limited
  Use requirements." The page says a public disclosure "such as an in-product
  disclosure on the application homepage, or public FAQ" is enough for that
  purpose. It does not name the privacy policy as one of the examples. The privacy
  policy is still required to disclose the same practices.
- The privacy policy in this PR uses that sample wording. Read the page directly
  before relying on the exact punctuation.

### Testing mode and refresh tokens

- OAuth 2.0 overview
  (<https://developers.google.com/identity/protocols/oauth2>, fetched 2026-11-15):
  "A Google Cloud Platform project with an OAuth consent screen configured for an
  external user type and a publishing status of 'Testing' is issued a refresh
  token expiring in 7 days."
- The same page's text about the test-user limit was not found. The 100-user
  figure for Testing mode is not confirmed from a Google page in this research.

## What homedrive actually does (checked against the code)

Repository state read on 2026-11-15: `main` at `b8b425d`, and `origin/main` at
`a8bc95c` when the branch was created. The verification below is against the
branch base.

- **Licence.** `LICENSE` at the repository root is MIT (copyright "2026
  asnowfix"). GitHub reports the licence as `mit`
  (`gh repo view asnowfix/home-drive`). The warranty disclaimer is quoted in the
  terms of service.
- **Outbound network.** No hardcoded HTTP(S) URLs in `homedrive/**/*.go`, other
  than documentation strings. Google Drive API calls use the Drive v3 client
  library, with the token endpoint `google.Endpoint`
  (`homedrive/internal/rcloneclient/driveapi.go`,
  `homedrive/internal/rcloneclient/oauthvalidate.go`). No analytics, telemetry,
  crash-reporting or update-check dependency appears in `homedrive/go.mod`.
- **MQTT.** Off unless `mqtt.enabled` is true. The broker is the configured
  `mqtt.broker` (`homedrive/internal/config/config.go`,
  `homedrive/cmd/homedrive/agent.go`). Events carry a `path` field
  (`homedrive/internal/mqtt/events.go`).
- **HTTP control.** Default listen `127.0.0.1:6090`
  (`homedrive/internal/http/server.go`). A non-loopback listen address without
  `http.auth_token` makes the server refuse to start (fail closed), per
  `homedrive/internal/http/server.go` and `homedrive/cmd/homedrive/agent.go`.
  The `ctl` client only calls the configured address
  (`homedrive/cmd/homedrive/ctl.go`).
- **Credentials.** OAuth token, and the optional `client_id` and `client_secret`,
  are read from the remote's section in rclone.conf (`readOAuthConfig` in
  `homedrive/internal/rcloneclient/driveapi.go`). rclone's refresh path persists
  refreshed tokens through its config storage, per the earlier OAuth research
  (`homedrive/docs/oauth-troubleshooting.md`, read at rclone v1.75.0).
- **Deletes.** Remote deletes call rclone's `operations.DeleteFile`
  (`homedrive/internal/rcloneclient/rclonefs.go`). rclone's drive backend
  defaults `use_trash` to true (`backend/drive/drive.go` in rclone v1.75.2, read
  from the module cache). Not checked: whether homedrive overrides `use_trash`
  anywhere. A grep did not find it.
- **No embedded OAuth client.** No `*.apps.googleusercontent.com` or client
  secret string is in the repository (grep, 2026-11-15). `homedrive/README.md`
  tells users to use their own `client_id`/`client_secret` rather than
  rclone's shared default. rclone's own help text for the `drive` backend says
  the shared client ID "is being retired and will stop working during 2026"
  (`backend/drive/drive.go`, rclone v1.75.2).

## Discrepancy with the brief

The brief says homedrive "uses a Google OAuth client that the maintainer owns".
The repository does not ship that client. Each installation reads whatever
client ID and secret are in its own rclone.conf, and the README directs users to
create their own. The maintainer's own client (project `313423816598`, named in
`homedrive/docs/oauth-troubleshooting.md`) is used only by the maintainer's own
deployment, as far as the repository shows.

This matters for the question being answered:

- If each user creates their own OAuth client, each user is the only user of
  that client. Google's personal-use exemption is then closer to the actual
  situation, and that user's own Google Cloud project is the one whose consent
  screen and publishing status apply.
- If the maintainer's client is published for others to use, the verification
  and user-cap rules above apply to that client.

The documents in this directory describe homedrive's real behaviour and work
for either model. The Branding-page URLs in the PR body apply only if the
maintainer publishes the maintainer's client.

## Observations (not a plan)

- The homepage requirement needs a publicly reachable page on the verified
  domain that describes the app and links the privacy policy. The PR adds no
  homepage. No such page exists in the repository or on the verified domain yet.
- The legal documents live under `homedrive/docs/legal/`. GitHub Pages serves a
  configured source folder or a workflow's output, so the URLs in the PR body do
  not resolve until Pages is configured to publish these files.
- The sample Limited Use statement and the privacy policy's wording should be
  checked by the maintainer against the live page before publishing.

## What could not be established

- **Whether Google Cloud Console accepts `asnowfix.github.io` as an authorized
  domain.** The top private domain analysis above points that way, but no Google
  page says so for a shared-suffix host. Test it in the Console and in Search
  Console.
- **Whether a `github.com` homepage is rejected by the Console.** Google's docs
  state the homepage must be on a verified domain you own. They do not mention
  GitHub. The conclusion that it fails is by that rule, not by a documented
  rejection.
- **Whether CASA (the security assessment) is required for this app.** See the
  section above. Google's own wording is not precise enough to settle it for an
  app with no developer-owned server.
- **Whether publishing a personal OAuth client to production, without
  verification, is allowed for personal use.** The exemption text covers apps
  used by the developer and a few known users. It does not say whether that
  exemption also covers a published (non-Testing) status. rclone's own docs
  recommend publishing rather than staying in Testing, to avoid the 7-day expiry
  (<https://rclone.org/drive/#making-your-own-client-id>, third-party guidance,
  not a Google statement).
- **Whether the Branding-page "Terms of service" field is mandatory in the
  Console.** The Google page says the ToS link is optional. The Console UI was not
  viewed.
- **How long verification takes.** Google's pages give no timeline. A search
  summary claimed "several weeks" to "several months" for restricted scopes. That
  summary has no primary source and is not used here.
- **Whether the Limited Use sample sentence matches the live page exactly.** The
  page was read through a summarizing fetch.
- **Whether a 100-user lifetime cap applies to unverified apps in Testing mode.**
  The unverified-app page ties the cap to apps that show the unverified-app
  screen. It does not say how Testing mode is treated.

## When I looked

- 2026-11-15, Google pages listed above. Pages with a date show that date.
- Public Suffix List file version `2026-11-14_13-54-10_UTC`.
- Repository state: `main` at `b8b425d`; `origin/main` at `a8bc95c` at branch
  creation. Branch `docs/legal-oauth-branding`.
- rclone source: `github.com/rclone/rclone@v1.75.2` in the local module cache
  (`homedrive/go.mod`). The earlier OAuth research in
  `homedrive/docs/oauth-troubleshooting.md` cites v1.75.0, which is now out of
  date. The pin moved to v1.75.2 since that research.
