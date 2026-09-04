// sslState.ts — shared rendering of a domain's computed SSL state.
//
// The state string comes from panel-api
// (internal/repository/domain_repository.go computeSSLState): "active_le"
// (valid LE cert), "self_signed", "pending", "issuing", "renewing",
// "pending_acme_retry", "failed", "revoked", "off". Extracted from
// UserDomainList so the domains list and the Mail Domains list (GH #1387)
// render the SSL badge identically.

export const getSSLTagColor = (state?: string): string => {
  switch (state) {
    case "active_le":
      return "gold"; // Let's Encrypt rendered yellow per operator request
    case "active":
    case "custom": // a valid admin-uploaded custom cert serves TLS
      return "green";
    case "provisioning":
      return "orange";
    case "self_signed":
      return "orange";
    case "pending":
    case "issuing":
    case "renewing":
    case "pending_acme_retry":
      return "green";
    case "failed":
    case "error":
    case "revoked":
      return "red";
    // "off"/"none"/""/undefined and any unknown state → neutral tag.
    default:
      return "default";
  }
};

export const getSSLTagLabel = (state?: string): string => {
  switch (state) {
    case "active_le":
      return "Let's Encrypt";
    case "active":
      return "Active";
    case "none":
      return "None";
    case "provisioning":
      return "Self-signed…";
    case "self_signed":
      return "Self-signed";
    case "pending":
    case "issuing":
    case "renewing":
    case "pending_acme_retry":
      return "Issuing…";
    case "custom":
      return "Custom";
    case "failed":
    case "error":
      return "Failed";
    case "revoked":
      return "Revoked";
    // "off" is the flat wire's disabled state (computeSSLState); render it the
    // same as the empty/undefined case rather than the raw lowercase string.
    case "off":
    case "":
    case undefined:
      return "Off";
    default:
      return state;
  }
};

// SSLBadgeLike is the admin domain list's nested SSL wire shape (GET
// /admin/domains embeds `ssl: { status, issuer, … }`), as opposed to the
// tenant list's flat `ssl_state` string. The admin backend spells a valid
// certificate `status: "issued"`; the flat wire spells the same thing
// `active_le`. JAB-300 folds both onto one color/label matrix so the two
// domain screens can't drift (admin used to render Let's Encrypt green while
// the tenant/Mail lists render it gold per the operator's request).
export type SSLBadgeLike = {
  status?: string | null;
  issuer?: string | null;
} | null | undefined;

// nestedStatusToFlat maps the admin nested-badge vocabulary onto the canonical
// flat `ssl_state` vocabulary the matrix above is keyed on. Only "issued"
// diverges — every other admin status (self_signed, pending, issuing,
// renewing, pending_acme_retry, failed, revoked, none, provisioning) already
// matches the flat spelling, so an unmapped status passes through unchanged.
const nestedStatusToFlat: Record<string, string> = {
  issued: "active_le",
};

// normalizeSSLBadge reduces a nested admin SSL badge to the canonical flat
// state plus its issuer. A badge with no status (or a null badge) normalizes
// to "" — the "Off" case.
export const normalizeSSLBadge = (
  ssl: SSLBadgeLike,
): { state: string; issuer?: string | null } => {
  if (!ssl || !ssl.status) return { state: "" };
  return { state: nestedStatusToFlat[ssl.status] ?? ssl.status, issuer: ssl.issuer };
};

// getSSLTag resolves the Tag color + label for EITHER a flat `ssl_state`
// string or an admin nested SSL badge — the single entry point both domain
// screens render through. For an active certificate the badge's issuer (e.g. a
// custom-uploaded cert's CN) overrides the generic label, preserving the admin
// list's behaviour of showing the issuer name on a good cert.
export const getSSLTag = (
  input: string | SSLBadgeLike,
): { color: string; label: string } => {
  let state: string;
  let issuer: string | null | undefined;
  if (input && typeof input === "object") {
    ({ state, issuer } = normalizeSSLBadge(input));
  } else {
    state = input ?? "";
  }
  const activeWithIssuer = (state === "active_le" || state === "active") && !!issuer;
  return {
    color: getSSLTagColor(state),
    label: activeWithIssuer ? (issuer as string) : getSSLTagLabel(state),
  };
};
