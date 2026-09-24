# Stalwart Sieve delivery — operator verification runbook

Covers **GH #1795**: proving that a mailbox's external forwards and
autoresponder actually run **at delivery**. Read the handler comments in
`panel-agent/internal/commands/mailbox_sieve_apply.go` and
`panel-api/internal/forwarderops/forwarderops.go` for the design, and
[[feedback_applyplan_converger_drift]] for why the interpreter limits live in
two files.

## The invariant this verifies

Stalwart runs exactly ONE store at delivery: the account's **active standard
`SieveScript`** (RFC 9661, capability `urn:ietf:params:jmap:sieve`). It does
**not** run `x:SieveUserScript` (a Stalwart extension store) at delivery. jabali
historically wrote external forwards to `x:SieveUserScript`, so forwards
silently no-oped. The fix composes forwards + the autoresponder into ONE active
standard `SieveScript` named `jabali-managed`.

Two failure modes to guard against on a real box:

1. **Wrong store** — a redirect written to `x:SieveUserScript` never fires.
2. **Update-in-place refusal** — a second apply updates the already-active
   `jabali-managed` script's `blobId`; a server that refuses this would leave the
   first script's content live. This runbook exercises create THEN update.

## Prerequisites

- A test box running the fleet Stalwart version (`stalwart --version`; #1795 was
  proven on **0.16.15**). The `.60` test box (`ssh jabalitests`, root) is the
  reference; see [[reference_smoke_host_2026_08]].
- The agent binary carrying `mailbox.sieve.apply` deployed and
  `jabali-agent` restarted (a build lacking the verb answers
  `unknownMethod`).
- The loopback JMAP admin endpoint on `127.0.0.1:8446` and the recovery-admin
  credentials the agent uses (`/etc/jabali-panel/…`; the same
  `stalwartAdminTokenFunc` seam the code reads).

## A. Drive the composite through the panel (real path)

1. In the panel, add TWO external forwards on a test mailbox
   (`user@testdomain`) — e.g. `a@out.example` and `b@out.example` with
   *keep a copy* on one — and enable an autoresponder with a subject + body.
2. Confirm the panel dispatched `mailbox.sieve.apply` (not the superseded
   `forwarder.apply` / `autoresponder.set`):

   ```
   journalctl -u jabali-agent --since "-2 min" | grep -i sieve
   ```

## B. Inspect the resulting script store (JMAP, no inbound mail needed)

Resolve the account id, then read its standard scripts. `ACCT` is the
`x:Account` id for the mailbox (from `x:Account/query`).

```bash
# List standard SieveScripts — expect exactly one named jabali-managed, active.
curl -s -u "admin:$TOKEN" http://127.0.0.1:8446/jmap \
  -H 'Content-Type: application/json' -d '{
    "using":["urn:ietf:params:jmap:core","urn:ietf:params:jmap:sieve"],
    "methodCalls":[["SieveScript/get",{"accountId":"ACCT"},"c0"]]}' | jq '.methodResponses'
```

Expect: one script, `name":"jabali-managed"`, `isActive":true`. Fetch its blob
and confirm it carries `redirect "a@out.example";`,
`redirect :copy "b@out.example";`, and a `vacation :mime` action.

```bash
# The legacy store must be EMPTY (the handler destroys it on every apply).
curl -s -u "admin:$TOKEN" http://127.0.0.1:8446/jmap \
  -H 'Content-Type: application/json' -d '{
    "using":["urn:ietf:params:jmap:core","urn:stalwart:jmap"],
    "methodCalls":[["x:SieveUserScript/get",{"accountId":"ACCT"},"c0"]]}' | jq '.methodResponses[0][1].list'
```

Expect: `[]`.

## C. Update-in-place (the second-apply path)

Change one forward target (or toggle keep-copy) in the panel and save again.
Re-run the `SieveScript/get` from step B. Expect: STILL exactly one script,
SAME id, `isActive":true`, and the blob now reflects the change. A new id, a
second script, or `isActive":false` means the update path regressed — capture
the `SieveScript/set` response's `notUpdated` and stop.

## D. End-to-end delivery (only where the box can send outbound :25)

On a box permitted to reach the internet on :25 (NOT a dev VPS that blocks it —
see [[project_gh1795_forwarding_sendonly]]), send a message to
`user@testdomain` and watch the delivery log for a remote queue entry:

```
tail -f /var/log/stalwart/delivery.log | grep -E 'delivery.completed|queueName'
```

Expect `delivery.completed` with `queueName":"remote"` (the redirect fired). An
`x:SieveUserScript` redirect produces `delivery.completed total=1` with **no**
remote queue entry — that is the exact wrong-store symptom #1795 fixes.

## Interpreter limits (multi-target forwards)

Stalwart's untrusted `x:SieveUserInterpreter` defaults `maxRedirects=1`,
`maxOutMessages=3`, which silently drops all but the first redirect of a
multi-target forward. jabali raises these to 20 / 25 in BOTH
`install/stalwart/apply-plan.json.tmpl` (fresh install) and the `install.sh`
converger (existing boxes). Verify on a box:

```
stalwart-cli get x:SieveUserInterpreter --json | jq '{maxRedirects,maxOutMessages}'
```

`TestApplyPlanSieveInterpreterParity` fails the build if the two files ever
disagree.
