// channelKindConfig.tsx — per-kind form fields + palette for the M14
// notification channel drawer. Keeps the dynamic form code in
// AdminChannelDrawer.tsx short; adding a new channel kind is a diff to
// this file only.

export type ChannelKind =
  | "email"
  | "slack"
  | "discord"
  | "ntfy"
  | "webhook"
  | "webpush"
  | "sms"
  | "telegram";

// CHANNEL_KINDS — kinds the admin can pick when creating a channel.
// "webpush" lives in the type union (and the kindLabels/kindFields
// maps) so existing rows keep rendering, but it's intentionally absent
// here: web push is per-browser and managed from the Notifications →
// Web Push tab, not as a configurable channel row.
export const CHANNEL_KINDS: ChannelKind[] = [
  "email",
  "slack",
  "discord",
  "ntfy",
  "webhook",
  "sms",
  "telegram",
];

export const kindColors: Record<ChannelKind, string> = {
  email: "blue",
  slack: "purple",
  discord: "geekblue",
  ntfy: "cyan",
  webhook: "gold",
  webpush: "green",
  sms: "magenta",
  telegram: "blue",
};

export const kindLabels: Record<ChannelKind, string> = {
  email: "Email",
  slack: "Slack",
  discord: "Discord",
  ntfy: "ntfy.sh",
  webhook: "Generic webhook",
  webpush: "Web Push (browser)",
  sms: "SMS (gateway)",
  telegram: "Telegram",
};

// ChannelFormFields — which per-kind config fields to render. The
// drawer renders inputs in this order. Each field is bound to the
// NotificationChannelConfig JSON blob on the row. `dependsOn` hides
// the field unless another config field equals the listed value —
// used by the email kind's external-SMTP fan-out so the relay
// host/port/credentials only show after the user picks "External SMTP".
export type FieldSpec = {
  name: keyof ChannelFormConfig;
  label: string;
  placeholder?: string;
  type?: "text" | "number" | "password" | "tags" | "select";
  required?: boolean;
  help?: string;
  options?: { value: string; label: string }[];
  dependsOn?: { name: keyof ChannelFormConfig; value: string };
};

export type ChannelFormConfig = {
  url?: string;
  bearer?: string;
  hmac_secret?: string;
  priority?: number;
  tags?: string[];
  to_email?: string;
  from_email?: string;
  smtp_mode?: "local" | "smtp";
  smtp_host?: string;
  smtp_port?: number;
  smtp_username?: string;
  smtp_password?: string;
  smtp_tls?: "starttls" | "tls" | "none";
  to_number?: string;
  bot_token?: string;
  chat_id?: string;
};

export const kindFields: Record<ChannelKind, FieldSpec[]> = {
  email: [
    { name: "to_email", label: "Recipient", placeholder: "admin@example.com", required: true },
    { name: "from_email", label: "Envelope sender", placeholder: "jabali@example.com", required: true },
    {
      name: "smtp_mode",
      label: "Delivery",
      type: "select",
      options: [
        { value: "local", label: "Local Stalwart (default)" },
        { value: "smtp", label: "External SMTP server" },
      ],
      help: "Local relays through the panel's loopback Stalwart submission port. External SMTP is for Gmail, SendGrid, Mailgun, etc.",
    },
    {
      name: "smtp_host",
      label: "SMTP host",
      placeholder: "smtp.gmail.com",
      required: true,
      dependsOn: { name: "smtp_mode", value: "smtp" },
    },
    {
      name: "smtp_port",
      label: "SMTP port",
      type: "number",
      placeholder: "587",
      required: true,
      help: "587 for STARTTLS, 465 for implicit TLS, 25 for plaintext.",
      dependsOn: { name: "smtp_mode", value: "smtp" },
    },
    {
      name: "smtp_tls",
      label: "TLS mode",
      type: "select",
      options: [
        { value: "starttls", label: "STARTTLS (port 587)" },
        { value: "tls", label: "Implicit TLS (port 465)" },
        { value: "none", label: "None (plaintext)" },
      ],
      dependsOn: { name: "smtp_mode", value: "smtp" },
    },
    {
      name: "smtp_username",
      label: "SMTP username",
      placeholder: "apikey or full email",
      dependsOn: { name: "smtp_mode", value: "smtp" },
    },
    {
      name: "smtp_password",
      label: "SMTP password",
      type: "password",
      dependsOn: { name: "smtp_mode", value: "smtp" },
    },
  ],
  slack: [
    { name: "url", label: "Webhook URL", placeholder: "https://hooks.slack.com/services/…", required: true },
  ],
  discord: [
    { name: "url", label: "Webhook URL", placeholder: "https://discord.com/api/webhooks/…", required: true },
  ],
  ntfy: [
    { name: "url", label: "Topic URL", placeholder: "https://ntfy.sh/your-topic", required: true },
    { name: "bearer", label: "Bearer token (optional)", type: "password", help: "Sent as Authorization: Bearer …" },
    { name: "priority", label: "Priority (1–5, optional)", type: "number" },
    { name: "tags", label: "Tags (optional)", type: "tags", help: "Comma-separated, e.g. warning,fire" },
  ],
  webhook: [
    { name: "url", label: "Target URL", placeholder: "https://example.com/hooks/jabali", required: true },
    {
      name: "hmac_secret",
      label: "HMAC secret",
      type: "password",
      required: true,
      help: "≥16 chars. Shared with the receiver, used to sign X-Jabali-Signature.",
    },
  ],
  webpush: [],
  sms: [
    {
      name: "url",
      label: "Gateway URL",
      placeholder: "https://example.com/sms-gateway",
      required: true,
      help: "Endpoint that accepts POST {to, body} JSON and forwards to your SMS provider (Twilio, MessageBird, etc.).",
    },
    {
      name: "to_number",
      label: "Recipient number",
      placeholder: "+15551234567",
      required: true,
      help: "E.164 format. Sent as the JSON 'to' field.",
    },
    {
      name: "hmac_secret",
      label: "HMAC secret (optional)",
      type: "password",
      help: "≥16 chars when set. Used to sign X-Jabali-Signature so the gateway can verify the call originated here.",
    },
    {
      name: "bearer",
      label: "Bearer token (optional)",
      type: "password",
      help: "Sent as Authorization: Bearer … if your gateway authenticates that way.",
    },
  ],
  telegram: [
    {
      name: "bot_token",
      label: "Bot token",
      type: "password",
      required: true,
      help: "From @BotFather. Create a bot with /newbot, copy the token.",
    },
    {
      name: "chat_id",
      label: "Chat ID",
      placeholder: "123456789 or -1001234567890",
      required: true,
      help: "The chat/group/channel to post to. Message the bot, then check https://api.telegram.org/bot<token>/getUpdates for your chat id.",
    },
  ],
};
