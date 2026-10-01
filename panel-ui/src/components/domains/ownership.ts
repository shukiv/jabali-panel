// GH #1816 / ADR-0170 — the ownership-proof wire shape and the texts the
// tenant and admin screens share (DomainOwnershipPanel, DomainOwnershipPage).

export type OwnershipView = {
  status: string;
  method: string;
  challenge_name: string;
  challenge_value: string;
  last_result: string;
  pending_since?: string | null;
  verified_at?: string | null;
  checked_at?: string | null;
  next_check_at?: string | null;
  expires_at?: string | null;
};

// isOwnershipPending: anything but "verified" is pending, as on the server. A
// response without the field (an older server) shows nothing.
export const isOwnershipPending = (d?: { ownership_status?: string } | null): boolean =>
  !!d && d.ownership_status !== undefined && d.ownership_status !== "verified";

// OWNERSHIP_RESULT_TEXT explains each check result to the owner.
export const OWNERSHIP_RESULT_TEXT: Record<string, string> = {
  verified: "Verified.",
  not_found: "No record found yet. A new DNS record can take a few minutes to appear.",
  propagating: "One public resolver sees the record; waiting for a second one to agree.",
  mismatch: "A TXT record exists, but its value is different. Copy the value exactly.",
  ns_points_here:
    "The domain's nameservers already point to this server, so a DNS record cannot prove ownership. An administrator must approve this domain.",
  dns_unresolvable:
    "The domain's DNS does not answer. Fix its nameservers at the registrar, or ask an administrator to approve it.",
  resolvers_unreachable: "This server could not reach the public DNS resolvers. It tries again on its own.",
};

export const OWNERSHIP_METHOD_TEXT: Record<string, string> = {
  dns_txt: "DNS record",
  admin: "administrator",
  parent: "parent domain",
  legacy: "existing before proof was required",
  migration: "migration",
  restore: "backup restore",
  automation: "billing system",
  policy_off: "added while proof was off",
};
