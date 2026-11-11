# Owner web workspace (fork feature)

Goal: an owner signs in on the web, writes a text request to Muse, explicitly
reviews a held request, and reads its real relay status and reply. The VPS hosts
the relay and the app; a user's Mac does not need to remain running. Muse must
still have an active receiver in its own sandbox. Requests can queue while it
is offline and can expire; this interface does not make Muse an always-on
service or silently create a schedule.

This is a single-owner first version, opt-in and disabled by default. It is not
a multi-user service or an ordinary agent's tool. The owner web process has
access to the relay's local admin capability and must be protected as such.

## How it connects

The opt-in app runs in the relay process with its own loopback-only HTTP
listener. Requests use an internal virtual `phone` identity (normally
`owner-web`), which asks but cannot receive tasks or administer the relay. Its
name cannot replace an existing agent or gateway identity. The new identity is
recorded in the relay audit log when it is first provisioned.

The web backend uses a separate in-process admin client for the fixed owner
history query and for explicit approve/deny actions. Browser API routes never
proxy arbitrary relay paths or accept a caller-selected identity. The first
version can only ask configured targets (initially Muse), list/read its own
requests, and approve or deny its own held requests. No agent tools can log in,
invite, change settings or approve through the owner app without the owner's
passphrase and authenticated browser session.

## Prepare credentials — owner action

Choose an unused internal owner identity and an HTTPS address on hosting you
control. A dedicated subdomain of an existing domain keeps this app isolated
from other sites on that host. No new domain purchase is necessary. A path
such as `/tincan/` also works, but applications on the same origin share a
browser trust boundary. Do not expose the relay API or its admin socket.

Run the tested fork binary in an interactive terminal:

```sh
tincan owner-web setup \
  --config /private/path/owner-web.json \
  --public-url https://YOUR_EXISTING_HOST/tincan/ \
  --listen 127.0.0.1:8788 --agent owner-web --target muse
```

The owner types and repeats a passphrase at a hidden prompt. It is never placed
in chat or command arguments. The config is created with mode 0600, refuses to
overwrite any existing path, and stores only a bcrypt hash. The passphrase is
16–72 UTF-8 bytes; production hashes use cost 12. `owner-web hash-password` is
also available for an explicit manual configuration workflow.

Do not use the integration-preview password or a `dev` config in production.
Dev mode accepts HTTP only on loopback. Production requires HTTPS and a
configured host; wildcard and non-loopback listeners are refused.

## Deployment review

Before enabling this on an existing VPS, review all of these concrete changes:

1. The exact binary, owner config path and HTTPS address.
2. Add `--owner-web-config /private/path/owner-web.json` to the existing relay
   service. The service user must be able to read that private config.
3. Starting the opt-in app provisions only its unused ask-only owner identity
   and creates `owner_web_submissions` in the relay's existing SQLite database.
4. Route only the owner app URL to its loopback listener through the existing
   HTTPS proxy. For a `/tincan/` prefix, preserve that prefix and the original
   Host header. The configured `public_url` must match exactly. Never route
   `/v1/`, the admin socket, or another project's private services.
5. Restart the pilot relay in a bounded maintenance window, verify the private
   gate is unchanged, then run one harmless real-Muse request and clean up any
   temporary fixture. No agent or public listener is enabled by local tests.

An example Caddy handler inside the chosen existing site's block is:

```caddyfile
handle /tincan/* {
    reverse_proxy 127.0.0.1:8788
}
```

This example is not an installer and must be reconciled with existing routes.
Do not use `handle_path`, which strips the prefix the app validates.

Rollback: remove the owner-web flag and its HTTPS proxy route and restart the
relay. This disables owner sign-in without touching Muse or the approval
policy. Existing requests remain in the relay. To remove the owner identity
too, explicitly run `tincan remove owner-web` on an admin device or local
socket. Keep the submission ledger: dropping it can lose duplicate protection.

## Security and delivery behavior

- Passphrases are checked with bcrypt; login is globally limited to five
  attempts per minute, with at most two hash checks in flight. No signup,
  password reset, email sender or external identity provider is enabled.
- Sessions use random 256-bit IDs stored as hashes in server memory, expire
  after 12 hours, and are revoked by sign-out or service restart. Production
  cookies are Secure, HttpOnly and SameSite=Strict with no Domain attribute.
  Root-hosted apps use the __Host- cookie prefix; a path-hosted app uses the
  __Secure- prefix with its configured cookie path.
- The configured Host is checked on every route. Changes require the exact
  Origin, JSON, a same-origin UI header and a per-session CSRF token. Scripts
  and connections are restricted to the app's origin; no third-party assets
  or analytics are loaded. All request and reply text is rendered as text.
- A held request is fetched in full before review. Approve and deny each need
  a separate short-lived one-use token bound to the session, request, decision
  and body hash. Attachments and follow-on requests require the existing admin
  tools; this first version cannot approve them. A send never auto-approves.
- Submission keys are persisted before forwarding. A repeated key returns
  the original result. Pending/uncertain outcomes stay blocked across restart,
  so a lost response cannot trigger an automatic resend. Check history before
  deliberately starting a new request. Recent history shows 20 requests.
- Relay errors are presented without internal paths, credentials or raw error
  details. Offline does not mean a request was cancelled. The app does not
  retry asks, install Muse processes or weaken the approval gate.

Review boundaries follow the upstream [trust model](trust-model.md). Session
and CSRF controls were checked against the primary
[OWASP session guidance](https://cheatsheetseries.owasp.org/cheatsheets/Session_Management_Cheat_Sheet.html)
and [CSRF guidance](https://cheatsheetseries.owasp.org/cheatsheets/Cross-Site_Request_Forgery_Prevention_Cheat_Sheet.html).

## Local verification

```sh
go test -race ./internal/ownerweb ./internal/identity ./internal/relay ./internal/cli
go vet ./...
node --check internal/ownerweb/static/app.js
```

The owner-web integration tests run the real relay, policy, SQLite storage and
identity code behind loopback-only test servers. The scripted Muse fixture is
not the real provider. It verifies authentication → held ask → explicit review
→ delivery → reply without using any external account.

For an optional interactive integration preview, build the test-only fixture:

```sh
go test -c -tags ownerwebpreview -o /tmp/tincan-owner-web-preview.test ./internal/ownerweb
TINCAN_OWNER_WEB_PREVIEW=1 \
TINCAN_OWNER_WEB_PREVIEW_INFO=/tmp/tincan-owner-preview.json \
  /tmp/tincan-owner-web-preview.test -test.run '^TestOwnerWebInteractivePreview$' -test.timeout 46m -test.v
```

The private preview-info file contains its loopback URL and a fixture-only
passphrase. The UI labels the test and its scripted replies. The fixture ends
after 45 minutes or SIGTERM. It never connects to external Muse, and is not
compiled into the production binary.
