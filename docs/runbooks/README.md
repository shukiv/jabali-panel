# Runbooks

Operational playbooks for things that go wrong in production, or
one-off setup that's too specific for the install script.

If you've just done something for the second time and it took more
than ten minutes to remember how, it's runbook-worthy.

## Index

| Runbook | When to use |
|---------|-------------|
| [php-fpm-pools.md](php-fpm-pools.md) | Adding/removing PHP versions, diagnosing 502/504 errors on PHP domains, verifying pool bindings. |
| [per-user-slices.md](per-user-slices.md) | Per-user systemd slice hierarchy — what's captured (FPM, shells, user timers) vs. not (traditional crontabs), cron→systemd-user-timer migration, troubleshooting the FPM master + cutover rollback. |
| [dns-secondary-nameserver.md](dns-secondary-nameserver.md) | Bringing up a secondary nameserver (ns2) — registrar needs two NS records with different IPs. |
| [security-advisory.md](security-advisory.md) | Every server must act on a security fix within the hour: publish an advisory in the feed servers check hourly. |
| [hotfix-release.md](hotfix-release.md) | A fix must reach stable servers before the monthly promotion: hotfix branch, prebuilt release, promote, merge back. |
| [m49-audit-log.md](m49-audit-log.md) | Audit-log integrity verify / a chain break, unsealed-backlog recovery, retention pruning, incident querying (M49 / ADR-0106). |

## Runbook template

Every runbook follows the same four-part shape so on-call can skim and
act without reading prose:

1. **Symptom** — what the operator sees (error, alert, log line).
2. **Diagnosis** — the fastest commands to confirm root cause and
   rule out look-alikes.
3. **Fix** — numbered, copy-pasteable steps. Include the rollback.
4. **Verification** — how to tell it actually worked.

Keep code blocks runnable; don't wrap them in prose that changes the
command. If a step needs judgment, say so explicitly and link to the
decision criteria.

## When to add one

Add a runbook when:

- A fix procedure has been executed at least twice.
- The fix crosses process boundaries (panel ↔ agent ↔ nginx, panel ↔
  PowerDNS, etc.) and isn't captured by a single code path.
- The failure mode doesn't self-heal via the reconciler.

Things that **do not** need runbooks:

- Anything the reconciler handles on its own (domain vhost regen,
  SSL retry, DNS zone bootstrap).
- Developer-setup steps — those belong in [`CONTRIBUTING.md`](../CONTRIBUTING.md).
- Code architecture — those belong in [`adr/`](../adr/).
