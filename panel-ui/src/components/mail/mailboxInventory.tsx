// Mailbox Inventory module (JAB-333) — the one owner for the presentation and
// webmail-launch behavior the three mailbox inventories used to each copy:
//
//   - admin/mail/AdminMailPage.tsx          (server-wide)
//   - user/mail/tabs/MailboxesTab.tsx       (tenant, cross-domain)
//   - admin/domains/DomainMailboxesSection.tsx (per-domain)
//
// Adapters still own their data source and their role-specific columns/actions
// (admin owner filter, tenant groups + autoresponders, domain enable-email +
// create wizard). This module owns only what MUST stay identical: the quota /
// status presentation and the safe webmail launcher (JAB-354).

import { Progress, Tag, Tooltip } from "antd";

import { useMintMailboxSSO } from "../../hooks/useMailboxes";
import { feedback } from "../../lib/feedback";

// ---- byte formatting ----------------------------------------------------

// The three inventories each rolled their own formatBytes; AdminMailPage always
// printed a decimal ("500.0 MiB") while the others dropped it for values >= 10
// ("500 MiB"). This is the single owner — KiB/MiB (binary, matching the byte
// columns the API returns). (utils/bytes.ts `humanBytes` uses SI labels KB/MB
// on binary math; unifying the ~10 formatBytes copies across the app is a
// separate cleanup and intentionally out of scope here.)
export function formatMailboxBytes(n: number | null | undefined): string {
  if (!n || n <= 0) return "0 B";
  const units = ["B", "KiB", "MiB", "GiB", "TiB"];
  const i = Math.floor(Math.log(n) / Math.log(1024));
  const v = n / Math.pow(1024, i);
  return `${v.toFixed(v >= 10 || i === 0 ? 0 : 1)} ${units[i]}`;
}

// ---- column renderers ---------------------------------------------------

// Structural row shapes — AdminMailbox / MailboxRow / Mailbox all satisfy these
// without a forced canonical row type (TS structural typing keeps the adapters'
// own row types intact).
export interface MailboxQuotaRow {
  quota_bytes: number;
  last_usage_bytes?: number | null;
}

// renderMailboxQuota — the quota progress bar + "used / quota" tooltip, shared
// verbatim so the >=90%-is-exception threshold and the byte formatting can't
// drift between screens.
export function renderMailboxQuota(row: MailboxQuotaRow) {
  const quota = row.quota_bytes ?? 0;
  const used = row.last_usage_bytes ?? 0;
  const pct = quota > 0 ? Math.min(100, Math.round((used / quota) * 100)) : 0;
  return (
    <Tooltip title={`${formatMailboxBytes(used)} of ${formatMailboxBytes(quota)}`}>
      <Progress
        percent={pct}
        size="small"
        status={pct >= 90 ? "exception" : "normal"}
        format={() => `${formatMailboxBytes(used)} / ${formatMailboxBytes(quota)}`}
      />
    </Tooltip>
  );
}

// renderMailboxStatus — the active/disabled tag.
export function renderMailboxStatus(isDisabled: boolean | undefined | null) {
  return isDisabled ? (
    <Tag color="red">disabled</Tag>
  ) : (
    <Tag color="green">active</Tag>
  );
}

// ---- safe webmail launcher (JAB-354) ------------------------------------

// useMailboxWebmail is the single owner of the open-blank-popup ->
// sever-opener -> mint -> navigate flow. Keeping it in one place is the point:
// JAB-330 shipped the opener-severing fix by hand into each of the three
// copies, and a fourth copy is exactly how the security fix was originally
// forgotten. It also unifies the two behaviors that had drifted between the
// copies:
//
//   - Blocked popup: warn and DO NOT mint. Admin/tenant used to call the mint
//     even when window.open returned null, silently burning a one-shot SSO URL
//     the user could never see.
//   - Mint failure: surface the typed `detail` (e.g. the rotate-password hint)
//     consistently, via the themed feedback bridge.
export function useMailboxWebmail() {
  const sso = useMintMailboxSSO();

  const launch = (id: string) => {
    // window.open MUST run synchronously with the click or popup blockers
    // intercept it; pop a blank tab now, navigate it once the mint responds.
    const popup = window.open("about:blank", "_blank");
    if (!popup) {
      feedback.message.warning(
        "Browser blocked the webmail popup — allow popups for this site or click again.",
      );
      return; // never mint an SSO URL the user can't reach
    }
    // JAB-330: sever the opener link BEFORE the async mint so the webmail tab
    // can never reach back into the panel window (reverse tabnabbing). Must run
    // synchronously here. (noopener can't be passed to window.open — it returns
    // null and breaks the synchronous-popup-then-set-href trick.)
    try {
      popup.opener = null;
    } catch {
      // older Safari — best effort
    }
    sso.mutate(
      { id },
      {
        onSuccess: (data) => {
          if (data?.url) popup.location.href = data.url;
          else popup.close();
        },
        onError: (err: unknown) => {
          popup.close();
          const resp = (
            err as { response?: { data?: { error?: string; detail?: string } } }
          )?.response?.data;
          // Typed hint for pre-Step-8 mailboxes (password_enc NULL): tell the
          // user to rotate the password so SSO material gets populated.
          if (resp?.error === "sso_unavailable_rotate_password") {
            feedback.message.warning(
              resp.detail ?? "Rotate the mailbox password to enable webmail SSO.",
            );
            return;
          }
          feedback.message.error(
            resp?.detail ?? resp?.error ?? "Failed to open webmail",
          );
        },
      },
    );
  };

  return {
    launch,
    // Per-row pending flag for the action's spinner.
    isLaunching: (id: string) => sso.isPending && sso.variables?.id === id,
  };
}
