// Cloudflare Worker — legacy `jabali update` bridge: 301-redirects the jabali2
// paths on git.jabali-panel.com to GitHub (shukiv/jabali-panel).
//
// Deployed hosts have git.jabali-panel.com compiled in (git `origin` remote +
// release API base). `jabali update` does two ops there: (1) `git fetch origin
// main` (+ reset to origin/main or the `stable` tag) over git smart-HTTP, and
// (2) release tarball metadata via /api/v1/.../releases. We 301-redirect BOTH
// to GitHub. git follows the info/refs redirect and remaps upload-pack to the
// new host; Go's http client follows the release-API redirect. A redirect (vs
// proxying) sends the host straight to GitHub — no packfile/tarball bytes
// through the Worker.
//
// The forge moved retired Gitea → Codeberg → GitHub; GitHub is the sole source
// of truth after the 2026-07 cutover. The self-hosted org/repo (shukivaknin/
// jabali2) becomes shukiv/jabali-panel on GitHub, and the release API host
// changes from the forge root to api.github.com, so each prefix maps to an
// explicit GitHub target rather than a same-path passthrough.
//
// SCOPED to jabali2 paths only — the old Gitea still hosts other repos on this
// domain; a catch-all redirect would break them.

// Each rule: forge path prefix -> GitHub base it maps onto. Prefixes are
// disjoint; the release API is listed first for readability.
const RULES = [
  // release API: /api/v1/repos/shukivaknin/jabali2/releases?... -> api.github.com/repos/shukiv/jabali-panel/releases?...
  { from: "/api/v1/repos/shukivaknin/jabali2/", to: "https://api.github.com/repos/shukiv/jabali-panel/" },
  // git smart-HTTP: /shukivaknin/jabali2.git/... -> github.com/shukiv/jabali-panel.git/...
  { from: "/shukivaknin/jabali2.git/", to: "https://github.com/shukiv/jabali-panel.git/" },
  // info/refs (no .git suffix form): /shukivaknin/jabali2/info/refs -> github.com/shukiv/jabali-panel/info/refs
  { from: "/shukivaknin/jabali2/info/refs", to: "https://github.com/shukiv/jabali-panel/info/refs" },
];

export default {
  async fetch(request) {
    const url = new URL(request.url);
    const rule = RULES.find((r) => url.pathname.startsWith(r.from));
    if (!rule) {
      return new Response("jabali update bridge: path not redirected\n", {
        status: 404,
        headers: { "content-type": "text/plain" },
      });
    }
    // 301 to the mapped GitHub path, preserving the tail + query
    // (git-upload-pack params etc.). git + the Go updater both follow.
    const tail = url.pathname.slice(rule.from.length);
    return Response.redirect(rule.to + tail + url.search, 301);
  },
};
