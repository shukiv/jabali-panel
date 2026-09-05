// channelPolicy — the explicit audience policy for the notification-channel
// Module (JAB-336, ADR-0083). One neutral <ChannelDrawer> plus one channel
// inventory (buildChannelColumns + useChannelActions) render both the admin
// (server-wide) and the tenant (self-service) surfaces. Everything that differs
// between the two audiences is captured here as data and supplied by each
// shell's thin adapter — nothing about "admin vs tenant" lives in the Module.
//
// This is the explicit-adapter shape: the adapter states resource path, the
// effective creatable kinds, the owner-column policy, the default kind and the
// tenant email/routing rules. The Module never infers audience.
import {
  CHANNEL_KINDS,
  type ChannelFormConfig,
  type ChannelKind,
} from "../../utils/channelKindConfig";

// NotificationChannel — the canonical channel row. Admin rows may be server-wide
// (user_id null/undefined) or tenant-owned (user_id set); a tenant only ever sees
// its own rows. Both shells' previous local types collapse into this one.
export type NotificationChannel = {
  id: string;
  name: string;
  kind: ChannelKind;
  config: ChannelFormConfig;
  enabled: boolean;
  user_id?: string | null;
  created_at?: string;
  updated_at?: string;
};

// TENANT_KINDS — the safe default server allowlist for self-service channels,
// used only as a fallback (first paint / an older API without allowed_kinds).
// The real creatable set comes from the server's effective policy (JAB-326),
// passed to tenantChannelPolicy(). The server create-gate stays authoritative.
export const TENANT_KINDS: ChannelKind[] = ["ntfy", "telegram", "discord", "webpush"];

// ChannelNote — copy for the info alert a kind shows instead of config fields.
export type ChannelNote = { message: string; description: string };

export type ChannelPolicy = {
  /** REST resource, e.g. "admin/notifications/channels" or "me/…". AC2. */
  resourcePath: string;
  /** Kinds offered in the create picker. AC5 (tenant allowed-kind is explicit). */
  allowedKinds: readonly ChannelKind[];
  /** Pre-selected kind for a brand-new channel. */
  defaultKind: ChannelKind;
  /** <Form id> — stable per surface so the drawer footer's submit button binds. */
  formId: string;
  namePlaceholder: string;
  /** Owner column is admin-only (server-wide vs tenant-owned). AC4. */
  showOwnerColumn: boolean;
  /** SMTP port wants the full TCP range; the ntfy priority number wants 1–5. */
  smtpPortFullRange: boolean;
  /**
   * Tenant email is forced server-side to the caller's own account address, so
   * its destination/SMTP fields are hidden and a note is shown instead. AC5.
   */
  forceOwnEmail: boolean;
  webpushNote: ChannelNote;
  /** Only rendered when forceOwnEmail is true. */
  emailNote?: ChannelNote;
  /**
   * Toast copy after POST /:id/test. `delivered` is read from the response body
   * (a synchronous send reports true/false; an async one omits it).
   */
  testResult: (name: string, delivered: boolean | undefined) => string;
};

export const ADMIN_CHANNEL_POLICY: ChannelPolicy = {
  resourcePath: "admin/notifications/channels",
  allowedKinds: CHANNEL_KINDS,
  defaultKind: "slack",
  formId: "channel-form",
  namePlaceholder: "Ops Slack",
  showOwnerColumn: true,
  smtpPortFullRange: true,
  forceOwnEmail: false,
  webpushNote: {
    message: "Web Push has no admin-configured fields",
    description:
      "Subscriptions are created per-browser from the user bell. VAPID keys live in server settings.",
  },
  testResult: (name, delivered) =>
    delivered
      ? `Test delivered to ${name}`
      : `Test queued for ${name} — see the History tab for the result`,
};

// tenantChannelPolicy — the self-service policy for a given effective allowlist.
// The default kind is the first allowed kind so we never pre-select something the
// server would reject; an empty/absent allowlist falls back to TENANT_KINDS.
export function tenantChannelPolicy(allowedKinds?: readonly ChannelKind[]): ChannelPolicy {
  const kinds = allowedKinds && allowedKinds.length > 0 ? allowedKinds : TENANT_KINDS;
  return {
    resourcePath: "me/notifications/channels",
    allowedKinds: kinds,
    defaultKind: kinds[0] ?? "ntfy",
    formId: "my-channel-form",
    namePlaceholder: "My phone",
    showOwnerColumn: false,
    smtpPortFullRange: false,
    forceOwnEmail: true,
    webpushNote: {
      message: "Web Push has no fields to configure",
      description:
        "It delivers to this browser's push subscription, created from the notification bell.",
    },
    emailNote: {
      message: "Email delivers to your account address",
      description:
        "For security, tenant email notifications are always sent to your own account email over the local mail server — the destination and SMTP settings are fixed.",
    },
    testResult: (name) => `Test sent to ${name}`,
  };
}
