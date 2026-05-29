// Shared Playwright fixtures — one place to mock the panel's API surface
// so tests focus on UI behaviour, not transport wiring.
//
// `mockApi({ me, users })` installs route handlers for every endpoint
// the SPA calls in the happy path. Tests override individual routes
// (e.g. to simulate a 409 on create) by calling page.route() AFTER
// mockApi returns.
import type { Page, Route } from "@playwright/test";
import { test as base, expect } from "@playwright/test";

// ---------- type shape of the mock world ----------

export type MockUser = {
  id: string;
  email: string;
  name_first?: string;
  name_last?: string;
  is_admin: boolean;
  created_at: string;
  updated_at: string;
};

export type MockPackage = {
  id: string;
  name: string;
  disk_quota_mb: number;
  bandwidth_quota_mb: number;
  max_domains: number;
  max_email_accounts: number;
  max_databases: number;
  max_ftp_accounts: number;
  ssh_enabled: boolean;
  cgi_enabled: boolean;
  created_at: string;
  updated_at: string;
};

export type MockDomain = {
  id: string;
  user_id: string;
  name: string;
  doc_root: string;
  is_enabled: boolean;
  nginx_custom_directives: string;
  created_at: string;
  updated_at: string;
};

export type MockState = {
  // /me response (identity). null → treat as unauthenticated.
  me: MockUser | null;
  // users list returned by GET /users (the list endpoint); default []
  users?: MockUser[];
  packages?: MockPackage[];
  domains?: MockDomain[];
};

// ---------- Kratos flow fixtures ----------
// Minimal shape of a Kratos browser login flow — matches KratosFlow in
// panel-ui/src/kratos.ts. Fixed csrf_token so tests that snoop the submit
// body can assert it round-trips.

function kratosPasswordFlow(flowId: string) {
  const now = Date.now();
  return {
    id: flowId,
    type: "browser",
    expires_at: new Date(now + 10 * 60_000).toISOString(),
    issued_at: new Date(now).toISOString(),
    request_url: "http://localhost/.ory/self-service/login/browser",
    ui: {
      // Same-origin relative path. Real Kratos emits an absolute URL that
      // matches the browser's origin (via the /.ory nginx proxy); the mock
      // must match the Playwright baseURL too, otherwise Set-Cookie lands on
      // the wrong domain and page.context().cookies() can't see it.
      action: `/self-service/login?flow=${flowId}`,
      method: "POST",
      nodes: [
        {
          type: "input",
          group: "default",
          attributes: { name: "csrf_token", type: "hidden", value: "csrf-token-xyz", required: true },
        },
        {
          type: "input",
          group: "password",
          attributes: { name: "identifier", type: "text", required: true, autocomplete: "email" },
          meta: { label: { text: "Email" } },
        },
        {
          type: "input",
          group: "password",
          attributes: { name: "password", type: "password", required: true, autocomplete: "current-password" },
          meta: { label: { text: "Password" } },
        },
        {
          type: "input",
          group: "password",
          attributes: { name: "method", type: "submit", value: "password" },
          meta: { label: { text: "Sign in" } },
        },
      ],
    },
    requested_aal: "aal1",
  };
}

function kratosWhoami(u: MockUser) {
  return {
    id: `session-${u.id}`,
    identity: {
      id: `kratos-${u.id}`,
      schema_id: "default",
      traits: { email: u.email, is_admin: u.is_admin },
    },
  };
}

// ---------- core installer ----------

export async function mockApi(page: Page, initial: MockState): Promise<void> {
  // Mutable state. The SPA starts every test logged OUT — `session`
  // becomes the active identity only after a successful /auth/login.
  // `initial.me` is the persona the test wants to sign in AS; it's
  // matched against the login form's email to decide whether the login
  // call succeeds.
  const state: {
    expected: MockUser | null; // identity that login will accept
    session: MockUser | null; // currently signed-in user (null = logged out)
    users: MockUser[];
    packages: MockPackage[];
    domains: MockDomain[];
    accessToken: string | null;
  } = {
    expected: initial.me,
    session: null,
    users: initial.users ?? (initial.me ? [initial.me] : []),
    packages: initial.packages ?? [],
    domains: initial.domains ?? [],
    accessToken: null,
  };

  // ---- auth (M20 Kratos browser flow) ----
  // The SPA drives /.ory/* for login/logout. Kratos flow discovery + submit
  // are mocked here so signIn() keeps working for every test unchanged.
  // Legacy JWT /auth/login/logout/refresh routes below are vestigial — only
  // meaningful when cfg.Auth.Provider == "legacy", and the authProvider
  // never hits them in Kratos mode. Kept as fail-closed 401 stubs so an
  // accidental regression to legacy transport is visibly wrong.
  await page.route("**/.ory/self-service/login/browser", async (route) => {
    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(kratosPasswordFlow("flow-mock")),
    });
  });
  await page.route("**/.ory/self-service/login/flows*", async (route) => {
    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(kratosPasswordFlow("flow-mock")),
    });
  });
  await page.route("**/.ory/self-service/login?flow=*", async (route) => {
    const req = route.request();
    // The SPA's kratos.ts uses axios which defaults to JSON for object bodies.
    // Real Kratos accepts either encoding on its browser-flow submit endpoint;
    // the mock must as well or it silently 400s on every test.
    const submittedEmail = extractSubmittedField(req.postData(), "identifier");
    if (state.expected && submittedEmail === state.expected.email) {
      state.session = state.expected;
      state.accessToken = "mock-kratos-session";
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        headers: {
          "Set-Cookie": "ory_kratos_session=mock-session; Path=/; HttpOnly; SameSite=Lax",
        },
        body: JSON.stringify({ session: kratosWhoami(state.expected) }),
      });
    }
    // Real Kratos echoes the full flow back with the error in ui.messages
    // so the SPA's renderer can keep the form visible while showing the
    // error. Returning `nodes: []` makes Login.tsx's pickActiveGroup
    // come back null and render "No credential method is currently
    // configured" — wrong UX and wrong mock shape.
    const errorFlow = kratosPasswordFlow("flow-mock");
    errorFlow.ui.messages = [
      {
        id: 4000006,
        text: "The provided credentials are invalid, check for spelling mistakes in your password or username, email address, or phone number.",
        type: "error",
      },
    ];
    return route.fulfill({
      status: 400,
      contentType: "application/json",
      body: JSON.stringify(errorFlow),
    });
  });
  await page.route("**/.ory/sessions/whoami", async (route) => {
    if (state.session) {
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(kratosWhoami(state.session)),
      });
    }
    return route.fulfill({
      status: 401,
      contentType: "application/json",
      body: JSON.stringify({ error: { code: 401, message: "no session" } }),
    });
  });
  // MyProfile's Security card auto-initialises a Kratos settings
  // flow on mount (initSettingsFlow → POST). Without a mock, the
  // call hits real Kratos which 401s, the component then
  // `window.location.assign('/login')`s, and any /profile assertion
  // races against the redirect. Return a benign empty flow so the
  // component stays on the page in its loading state.
  await page.route("**/.ory/self-service/settings/api*", async (route) => {
    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        id: "mock-settings-flow",
        type: "api",
        ui: { action: "", method: "POST", nodes: [] },
      }),
    });
  });
  await page.route("**/.ory/self-service/settings/browser*", async (route) => {
    return route.fulfill({
      status: 303,
      headers: { Location: "/jabali-panel/profile?flow=mock-settings-flow" },
    });
  });
  await page.route("**/.ory/self-service/settings/flows*", async (route) => {
    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        id: "mock-settings-flow",
        type: "browser",
        ui: { action: "", method: "POST", nodes: [] },
      }),
    });
  });
  await page.route("**/.ory/self-service/logout/browser", async (route) => {
    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        // Same-origin so the Set-Cookie clearing below applies to the
        // Playwright baseURL origin (127.0.0.1). kratos.ts also reads
        // logout_token, so emit that too.
        logout_token: "mock-logout-token",
        logout_url: "/self-service/logout?token=mock-logout-token",
      }),
    });
  });
  await page.route("**/.ory/self-service/logout*token=*", async (route) => {
    state.session = null;
    state.accessToken = null;
    return route.fulfill({
      status: 302,
      headers: {
        Location: "/login",
        // Real Kratos clears the session cookie with Max-Age=0 on logout.
        // Without this, page.context().cookies() still shows the cookie
        // and the logout E2E assertion fails.
        "Set-Cookie": "ory_kratos_session=; Path=/; Max-Age=0; HttpOnly; SameSite=Lax",
      },
      body: "",
    });
  });

  // Legacy JWT endpoints — kept for tests that explicitly exercise the
  // "legacy" provider. Under Kratos default the SPA never calls them.
  await page.route("**/api/v1/auth/login", async (route) => {
    const req = route.request();
    const body = req.postDataJSON() as { email: string; password: string };
    if (state.expected && body.email === state.expected.email) {
      state.session = state.expected;
      state.accessToken = "mock-access-token";
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          access_token: state.accessToken,
          token_type: "Bearer",
          expires_in: 900,
          user: state.session,
        }),
      });
    }
    return route.fulfill({
      status: 401,
      contentType: "application/json",
      body: JSON.stringify({ error: "invalid_credentials" }),
    });
  });

  await page.route("**/api/v1/auth/logout", async (route) => {
    state.accessToken = null;
    state.session = null;
    return route.fulfill({ status: 200, contentType: "application/json", body: "{}" });
  });

  // Refresh requires an active session. This mirrors the real server —
  // the refresh cookie is set at login time and invalidated at logout,
  // so a cold page load (no prior login in this session) must 401.
  await page.route("**/api/v1/auth/refresh", async (route) => {
    if (state.session) {
      state.accessToken = "mock-access-token-refreshed";
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          access_token: state.accessToken,
          expires_in: 900,
        }),
      });
    }
    return route.fulfill({
      status: 401,
      contentType: "application/json",
      body: JSON.stringify({ error: "invalid_refresh" }),
    });
  });

  // ---- identity ----
  await page.route("**/api/v1/me", async (route) => {
    if (state.session && state.accessToken) {
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          id: state.session.id,
          email: state.session.email,
          is_admin: state.session.is_admin,
        }),
      });
    }
    return route.fulfill({
      status: 401,
      contentType: "application/json",
      body: JSON.stringify({ error: "missing_authorization" }),
    });
  });

  // ---- system (dashboard) ----
  await page.route("**/api/v1/system/info", async (route) => {
    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        hostname: "test-server",
        uptime_seconds: 86400,
        load_avg: [0.15, 0.10, 0.05],
        cpu_count: 4,
        mem_total_kb: 16384000,
        mem_available_kb: 8192000,
        mem_used_kb: 8192000,
        partitions: [
          {
            mount_point: "/",
            total_bytes: 53687091200,
            used_bytes: 21474836480,
            free_bytes: 32212254720,
          },
        ],
      }),
    });
  });

  await page.route("**/api/v1/system/services", async (route) => {
    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        services: [
          { name: "nginx", active: "active" },
          { name: "mariadb", active: "active" },
          { name: "php8.3-fpm", active: "active" },
          { name: "stalwart-mail", active: "inactive" },
        ],
      }),
    });
  });

  // ---- users CRUD ----
  await page.route(/\/api\/v1\/users(\?.*)?$/, async (route) => {
    const req = route.request();
    if (req.method() === "GET") {
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          data: state.users,
          total: state.users.length,
          page: 1,
          page_size: 20,
        }),
      });
    }
    if (req.method() === "POST") {
      const body = req.postDataJSON() as Partial<MockUser> & {
        password: string;
      };
      const now = new Date().toISOString();
      const newUser: MockUser = {
        id: `01${Math.random().toString(36).slice(2, 10).toUpperCase().padEnd(24, "0")}`,
        email: body.email ?? "",
        name_first: body.name_first ?? "",
        name_last: body.name_last ?? "",
        is_admin: body.is_admin ?? false,
        created_at: now,
        updated_at: now,
      };
      state.users.push(newUser);
      return route.fulfill({
        status: 201,
        contentType: "application/json",
        body: JSON.stringify(newUser),
      });
    }
    return unsupported(route);
  });

  await page.route(/\/api\/v1\/users\/[^/?]+(\?.*)?$/, async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const id = decodeURIComponent(url.pathname.split("/").pop() ?? "");
    const idx = state.users.findIndex((u) => u.id === id);

    if (req.method() === "GET") {
      if (idx < 0) return notFound(route);
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(state.users[idx]),
      });
    }
    if (req.method() === "PATCH") {
      if (idx < 0) return notFound(route);
      const patch = req.postDataJSON() as Partial<MockUser>;
      state.users[idx] = {
        ...state.users[idx],
        ...patch,
        updated_at: new Date().toISOString(),
      };
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(state.users[idx]),
      });
    }
    if (req.method() === "DELETE") {
      if (idx < 0) return notFound(route);
      state.users.splice(idx, 1);
      return route.fulfill({ status: 204, body: "" });
    }
    return unsupported(route);
  });

  // ---- M18 resource limits / live usage ----
  // MyProfileUsageCard and UserSliceStatus poll these on a 5-10s cadence.
  // Without mocks they proxy through vite → panel-api (which isn't up in
  // e2e) → ECONNREFUSED → TanStack retries → log floods + background
  // re-renders that can race with Playwright's actionability checks
  // during form interaction (see users-spec "create flow" flake — input
  // resolves in DOM but isn't "editable" within the timeout).
  await page.route(/\/api\/v1\/users\/[^/?]+\/usage(\?.*)?$/, async (route) => {
    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        user_id: "mock",
        effective: {
          DiskQuotaMB: 10240,
          CPUQuotaPercent: 100,
          MemoryLimitMB: 2048,
          IOReadMbps: 0,
          IOWriteMbps: 0,
          MaxTasks: 512,
        },
        // current omitted — UI handles its absence gracefully
      }),
    });
  });
  await page.route(
    /\/api\/v1\/admin\/users\/[^/?]+\/slice-status(\?.*)?$/,
    async (route) => {
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          username: "jabali-mock",
          slice_active: true,
          fpm_active: true,
          memory_current_bytes: 0,
          tasks_current: 0,
          cpu_usage_nsec: 0,
        }),
      });
    },
  );

  // ---- packages CRUD ----
  await page.route(/\/api\/v1\/packages(\?.*)?$/, async (route) => {
    const req = route.request();
    if (req.method() === "GET") {
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          data: state.packages,
          total: state.packages.length,
          page: 1,
          page_size: 20,
        }),
      });
    }
    if (req.method() === "POST") {
      const body = req.postDataJSON() as Partial<MockPackage>;
      const now = new Date().toISOString();
      const newPackage: MockPackage = {
        id: `01${Math.random().toString(36).slice(2, 10).toUpperCase().padEnd(24, "0")}`,
        name: body.name ?? "",
        disk_quota_mb: body.disk_quota_mb ?? 0,
        bandwidth_quota_mb: body.bandwidth_quota_mb ?? 0,
        max_domains: body.max_domains ?? 0,
        max_email_accounts: body.max_email_accounts ?? 0,
        max_databases: body.max_databases ?? 0,
        max_ftp_accounts: body.max_ftp_accounts ?? 0,
        ssh_enabled: body.ssh_enabled ?? false,
        cgi_enabled: body.cgi_enabled ?? false,
        created_at: now,
        updated_at: now,
      };
      state.packages.push(newPackage);
      return route.fulfill({
        status: 201,
        contentType: "application/json",
        body: JSON.stringify(newPackage),
      });
    }
    return unsupported(route);
  });

  await page.route(/\/api\/v1\/packages\/[^/?]+(\?.*)?$/, async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const id = decodeURIComponent(url.pathname.split("/").pop() ?? "");
    const idx = state.packages.findIndex((p) => p.id === id);

    if (req.method() === "GET") {
      if (idx < 0) return notFound(route);
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(state.packages[idx]),
      });
    }
    if (req.method() === "PATCH") {
      if (idx < 0) return notFound(route);
      const patch = req.postDataJSON() as Partial<MockPackage>;
      state.packages[idx] = {
        ...state.packages[idx],
        ...patch,
        updated_at: new Date().toISOString(),
      };
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(state.packages[idx]),
      });
    }
    if (req.method() === "DELETE") {
      if (idx < 0) return notFound(route);
      state.packages.splice(idx, 1);
      return route.fulfill({ status: 204, body: "" });
    }
    return unsupported(route);
  });

  // ---- domains CRUD ----
  await page.route(/\/api\/v1\/domains(\?.*)?$/, async (route) => {
    const req = route.request();
    if (req.method() === "GET") {
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          data: state.domains,
          total: state.domains.length,
          page: 1,
          page_size: 20,
        }),
      });
    }
    if (req.method() === "POST") {
      const body = req.postDataJSON() as Partial<MockDomain>;
      const now = new Date().toISOString();
      const newDomain: MockDomain = {
        id: `01${Math.random().toString(36).slice(2, 10).toUpperCase().padEnd(24, "0")}`,
        user_id: body.user_id ?? state.session?.id ?? "",
        name: body.name ?? "",
        doc_root: body.doc_root ?? "/var/www/" + (body.name ?? "example").replace(/\./g, "_"),
        is_enabled: body.is_enabled ?? true,
        nginx_custom_directives: body.nginx_custom_directives ?? "",
        created_at: now,
        updated_at: now,
      };
      state.domains.push(newDomain);
      return route.fulfill({
        status: 201,
        contentType: "application/json",
        body: JSON.stringify(newDomain),
      });
    }
    return unsupported(route);
  });

  await page.route(/\/api\/v1\/domains\/[^/?]+(\?.*)?$/, async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const id = decodeURIComponent(url.pathname.split("/").pop() ?? "");
    const idx = state.domains.findIndex((d) => d.id === id);

    if (req.method() === "GET") {
      if (idx < 0) return notFound(route);
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(state.domains[idx]),
      });
    }
    if (req.method() === "PATCH") {
      if (idx < 0) return notFound(route);
      const patch = req.postDataJSON() as Partial<MockDomain>;
      state.domains[idx] = {
        ...state.domains[idx],
        ...patch,
        updated_at: new Date().toISOString(),
      };
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(state.domains[idx]),
      });
    }
    if (req.method() === "DELETE") {
      if (idx < 0) return notFound(route);
      state.domains.splice(idx, 1);
      return route.fulfill({ status: 204, body: "" });
    }
    return unsupported(route);
  });
}

/**
 * Extract a named field from a POST body that may be JSON (axios default for
 * object bodies) or application/x-www-form-urlencoded (traditional form
 * submit). Kratos browser-flow endpoints accept either, and the SPA uses
 * JSON via axios — so the mock must handle both.
 */
export function extractSubmittedField(body: string | null | undefined, field: string): string {
  if (!body) return "";
  // Try JSON first — axios' default Content-Type for object bodies.
  const trimmed = body.trimStart();
  if (trimmed.startsWith("{")) {
    try {
      const parsed = JSON.parse(body) as Record<string, unknown>;
      const v = parsed[field];
      return typeof v === "string" ? v : v == null ? "" : String(v);
    } catch {
      // fall through to form-encoded parse
    }
  }
  // Fallback: form-URL-encoded.
  const re = new RegExp(`(?:^|&)${field}=([^&]*)`);
  const match = body.match(re);
  return match ? decodeURIComponent(match[1].replace(/\+/g, " ")) : "";
}

function notFound(route: Route) {
  return route.fulfill({
    status: 404,
    contentType: "application/json",
    body: JSON.stringify({ error: "not_found" }),
  });
}

function unsupported(route: Route) {
  return route.fulfill({
    status: 405,
    contentType: "application/json",
    body: JSON.stringify({ error: "method_not_allowed" }),
  });
}

// ---------- canned personas ----------

export const admin: MockUser = {
  id: "01KPADMIN00000000000000000",
  email: "admin@test.local",
  name_first: "Test",
  name_last: "Admin",
  is_admin: true,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

export const user: MockUser = {
  id: "01KPUSER000000000000000000",
  email: "user@test.local",
  name_first: "Test",
  name_last: "User",
  is_admin: false,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

// ---------- helper: sign in through the login form ----------

export async function signIn(page: Page, u: MockUser, password = "anypassword123"): Promise<void> {
  // Suppress the admin Quick Start modal that pops on first login. Without
  // this, every admin-shell spec races the modal's portal — it overlays
  // navigation and click targets and ~half the admin tests fail with
  // "element intercepts pointer events". Runs on every navigation so any
  // post-login redirect/reload still has the key primed before React reads
  // it. Real users see the modal once and dismiss it themselves.
  await page.addInitScript(
    ({ prefix, userId }) => {
      try {
        localStorage.setItem(prefix + userId, "1");
      } catch {
        // localStorage unavailable — modal will pop but only on this page.
      }
    },
    { prefix: "jabali:quickstart:dismissed:", userId: u.id },
  );
  await page.goto("/login");
  await page.getByLabel(/email/i).fill(u.email);
  await page.getByLabel(/password/i).fill(password);
  await page.getByRole("button", { name: /sign in/i }).click();
}

// Asserts the document is not horizontally scrolling. Used by the M23
// responsive spec on every page transition — a responsive regression
// almost always announces itself by making documentElement.scrollWidth
// exceed clientWidth. See ADR-0046.
export async function expectNoHorizontalOverflow(page: Page): Promise<void> {
  const hasOverflow = await page.evaluate(
    () =>
      document.documentElement.scrollWidth >
      document.documentElement.clientWidth,
  );
  expect(
    hasOverflow,
    "viewport is horizontally scrolling — table without scroll=max-content, fixed-width element, or overflow leak",
  ).toBe(false);
}

// Re-export expect so test files import everything from one place.
export { base as test, expect };
