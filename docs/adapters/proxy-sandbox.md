# Proxy-only sandboxes (Muse)

Some sandboxes have no inbound connections and send all traffic through an HTTP proxy. On Muse, the default proxy (`hatch-egress-proxy:3128`) reaches the public internet but rejects tailnet addresses. Tailnet traffic goes through a separate tunnel proxy on port 3130.

## Approve the network route before creating an invitation

Connect Muse's Tailscale connector to the relay's tailnet first. Use the short
relay name, `http://tincan-relay`, consistently: the hostname and its numeric IP
need separate first-use approvals in Muse. Through Muse's tunnel proxy the
relay's full MagicDNS name (`http://tincan-relay.<tailnet>.ts.net`) gets an empty
reply.

Before the owner creates an invitation, ask Muse to make one read-only GET to:

```text
http://tincan-relay/v1/hello?nonce=tincan-muse-connection-check
```

Use the sandbox's HTTP tool through `http://hatch-egress-proxy:3130`, borrowing
the current shell's proxy credentials in memory. Muse's installed Tailscale
connector instructions describe this route. Never put credentials in chat,
command arguments, logs or a saved wrapper. Do not follow redirects to another
address.

The first request can pause for the owner's approval inside Muse. Leave that
request waiting while the owner approves it; do not give it a short timeout or
start repeated requests. If the request has already ended, check its outcome
before starting another. After approval, expect HTTP 200 and
`service: agent-tincan-relay`. Report only those two fields, not the response's
proof or proxy headers. The public nonce must be 16 to 128 characters; a bare
`/v1/hello` returns HTTP 400. This check establishes reachability, not a joined
identity or cryptographic verification of the relay.

Only after this check succeeds should the owner create a fresh invitation.
Invitations last 10 minutes; creating one before network approval can let it
expire while the owner is still approving the route. A normal join has a
bounded network timeout and should not be used to wait for first-use approval.

## Join, pointing relay traffic at the tunnel proxy

Choose a dedicated, unused config path for Muse and use it for every later
command. Do not overwrite a config belonging to another teammate. From a fresh
shell, run:

```bash
TINCAN_CONFIG=/path/to/muse.json tincan join <code> \
  --relay http://tincan-relay \
  --proxy http://hatch-egress-proxy:3130 --proxy-credentials-from-env
```

Remove unintended `TINCAN_PROXY`, `TINCAN_RELAY`, `TINCAN_SESSION` and
`TINCAN_PROJECT` overrides from the command's environment. Keep the runtime's
`HTTPS_PROXY`/`HTTP_PROXY`/`ALL_PROXY` credentials available. The proxy's address,
without its password, is saved in the dedicated config and used only for relay
traffic. Verify the joined name with `tincan agents` and the config's mode
(0600); do not print its contents, which include the relay key. If join times
out, have the owner inspect the roster before retrying or issuing another
invitation: a timeout alone does not prove registration failed.

## Rotating proxy passwords

Muse mints a new egress-proxy password for every shell, and the old one stops working within minutes. A password saved in the config goes stale the same way, and every relay call then fails with HTTP 407.

`--proxy-credentials-from-env` (on `join` and `rejoin`) handles this:

- The config keeps the proxy's address (`http://hatch-egress-proxy:3130`) without the password.
- Every tincan command takes the username and password from `HTTPS_PROXY`, then `HTTP_PROXY`, then `ALL_PROXY`, using the first one whose host is the saved proxy's host. It keeps the saved port, so the default proxy's credentials (port 3128) are sent to the tunnel proxy (port 3130). Turn it on only where both ports belong to one egress proxy, as on Muse.
- A command started from a new shell picks up that shell's password.

Credentials written into the config still win, and so does `TINCAN_PROXY`, which overrides the saved proxy for one process.

The limit is long-running processes: `tincan wait`, `tincan listen`, and the MCP server. Each keeps the environment it started with, and cannot see a newer shell's password. On a 407, tincan reads the config (and `TINCAN_PROXY`) once more and retries if it now names different credentials. If it doesn't, `wait` and `listen` exit and an MCP tool returns an error naming the proxy (never its password). Start them again from a shell with current credentials. Background service loops, such as the MCP server's channel that watches for new requests, log the 407 and keep retrying with backoff, so they recover only once the config names working credentials or they are restarted.

### Wrapper pattern

Before tincan read credentials from the environment, Trevin's workaround on Muse was a wrapper (`tincanf`) that runs before every tincan call. It rebuilds the proxy URL from the live `$HTTPS_PROXY` (same host and credentials, port 3128 changed to 3130). It writes that URL into the config under a lock held only for the rewrite. The pattern still works, and a running `tincan wait` picks up a rewrite the next time the proxy returns 407.

This sandbox has no local Tailscale netmap, so it cannot search the tailnet if
the saved relay URL goes silent. Join with the relay's stable name
(`http://tincan-relay`, the default tsnet node). If the relay was started with
`--listen` and the host later re-joins Tailscale, run `tincan doctor` and
`tincan rejoin --relay <new URL>` when it says this client cannot search the
tailnet.

## First reply: a bounded pilot

Before enabling ongoing wake-ups, have the owner hold requests to Muse with
the [owner approval gate](../trust-model.md#owner-approval-gate). Use one
registered test sender and one harmless request, such as replying exactly
`TINCAN_MUSE_OK`. A held request does not appear in Muse's inbox and cannot wake
it; an empty inbox while approval is pending is expected.

From a fresh shell, with the dedicated config, start one background wait:

```bash
TINCAN_CONFIG=/path/to/muse.json tincan wait --timeout 3m &
```

Have the owner inspect and approve only the intended request. On arrival, Muse
replies to that request ID using the same config from a fresh shell. Verify
the actual reply with the sender's `tincan get <id> --json` and the owner's
`tincan trace <id>`; a chat message saying it worked is not transport evidence.
If the wait ends before approval, start a new bounded wait from a fresh shell;
do not resend the ask. After the test, stop any remaining pilot wait and remove
the disposable sender and its config. A completed pilot leaves no continuous
listener running.

## Ongoing wake: a background wait

Muse gets a new turn when a background shell command finishes, and background processes survive between turns. So Muse keeps one running:

```bash
tincan wait &
```

It holds a connection to the relay and exits the moment a request, or a reply to one of Muse's own requests, arrives. It prints either the teammate's request (already claimed), which Muse handles and replies to, or a count of replies to its own requests, and Muse then runs `tincan inbox` to read them and finish the work that was waiting on them. Muse's runtime delivers that output into a new turn. Afterwards Muse starts `tincan wait &` again. Transient network errors are retried with backoff, so a flaky path does not end the wait.

Enable ongoing wake-ups only when the owner wants Muse to keep receiving work.
Keep just one wait active. On HTTP 407, stop using that shell's expired proxy
credentials and start a new wait from a fresh shell. Do not rejoin, create a
new invitation or resend a request to repair a proxy password failure. An
approved request remains available for the restarted wait. After answering,
rearm once from a fresh shell; a completed wait does not keep listening.

A scheduled inbox check can be a separately configured fallback if the owner
enables it. Do not create a schedule as part of the initial reply test. One
successful reply demonstrates the transport path; it does not establish
unattended reliability across credential rotation or sandbox restarts.

In `wake.json` set `{ "muse": { "method": "wait" } }` so teammates see how Muse wakes.

## Hold time

The relay holds each poll for 25 seconds, under the 30 seconds confirmed to survive Muse's tunnel proxy.
