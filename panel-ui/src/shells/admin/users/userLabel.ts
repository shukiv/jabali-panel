// userLabel — canonical admin-facing identifier for a user (GH #1239).
// Usernames are the login since M54; email is a plain trait that may be
// shared across accounts, so confirmation modals and toasts must lead
// with the username. Admins have no POSIX username — fall back to email.
// GH #1227 collapsed the display name into name_first; name_last is kept
// only for legacy rows.
export function userLabel(u: {
  username?: string | null;
  email: string;
  name_first?: string;
  name_last?: string;
}): string {
  const base = u.username || u.email;
  const name = [u.name_first, u.name_last].filter(Boolean).join(" ").trim();
  return name ? `${base} (${name})` : base;
}
