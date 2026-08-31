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
    case "failed":
    case "error":
      return "Failed";
    case "revoked":
      return "Revoked";
    case "":
    case undefined:
      return "Off";
    default:
      return state;
  }
};
