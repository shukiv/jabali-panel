// notificationTarget — GH #726: every notification needs a click destination.
//
// Kinds with a dedicated deeplink use it (panel update -> Updates, ssh_login ->
// Security). Kinds without one — service.down and other Error-severity alerts —
// used to dead-end on the dashboard when clicked. Fall back to the admin
// Notifications > History tab, focused on this row, so the click always lands
// somewhere useful.
export function notificationTarget(id: string, deeplink?: string | null): string {
  if (deeplink) return deeplink;
  return `/jabali-admin/notifications/history?focus=${encodeURIComponent(id)}`;
}
