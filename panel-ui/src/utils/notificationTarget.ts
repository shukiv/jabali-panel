// notificationTarget — GH #726: every notification needs a click destination.
//
// Kinds with a dedicated deeplink use it (panel update -> Updates, ssh_login ->
// Security). Kinds without one — service.down and other Error-severity alerts —
// used to dead-end on the dashboard when clicked. Fall back to the admin
// Notifications > History tab, focused on this row, so the click always lands
// somewhere useful.
export function notificationTarget(id: string, deeplink?: string | null): string {
  const link = panelPath(deeplink);
  if (link) return link;
  return `/jabali-admin/notifications/history?focus=${encodeURIComponent(id)}`;
}

// Same rule as the server's notifications.PanelPath: a "/"-rooted path inside
// the panel, not "//host", made only of letters, digits and URL punctuation.
const PANEL_PATH = /^\/(?![/\\])[A-Za-z0-9/\-._~?&=%+:@#!$()*,;]*$/;

// panelPath returns a notification link only when it is a path inside the
// panel, and null otherwise. The server already keeps links to panel paths;
// this also covers rows stored before it did.
export function panelPath(link?: string | null): string | null {
  if (!link || link.length > 2048 || !PANEL_PATH.test(link)) return null;
  return link;
}
