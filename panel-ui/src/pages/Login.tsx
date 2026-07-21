// Login page — driven by Ory Kratos browser self-service flow.
//
// On mount we call initLoginFlow() to get a fresh flow object (CSRF token
// + ui.nodes describing what inputs Kratos wants). We render those nodes
// generically, submit to flow.ui.action with the user-entered values, and
// either:
//   - {session} came back → login finished, navigate by role
//   - {flow} came back    → AAL2 step required (e.g. TOTP). Re-render.
//   - error               → surface the message, let user retry.
//
// Kratos does AAL2 by returning an updated flow whose nodes belong to a
// new group (usually "totp"). The renderer reads node.group and groups
// the form visually — each non-default group becomes its own submit
// button, matching the M5c behaviour the legacy panel had.
import { useEffect, useState } from "react";
import { useNavigate } from "react-router";
import {
  Alert,
  Button,
  Card,
  Form,
  Input,
  Space,
  Spin,
  Typography,
  theme,
} from "antd";
import { useQueryClient } from "@tanstack/react-query";

import { clearIdentity, getIdentity } from "../identity";
import type { MeUser } from "../auth/AuthContext";
import { useThemeMode } from "../theme/ThemeModeContext";

// Post-M21 inline: authProvider.ts used to export this. It's a
// trivial two-branch map, not worth its own module now that no
// provider context depends on it.
const ADMIN_HOME = "/jabali-admin";
const USER_HOME = "/jabali-panel";
function homeForRole(isAdmin: boolean): string {
  return isAdmin ? ADMIN_HOME : USER_HOME;
}
import {
  csrfToken,
  flowMessages,
  getLoginFlow,
  initLoginFlow,
  renderableFields,
  submitLoginFlow,
  type KratosFlow,
  type RenderableField,
} from "../kratos";

export const LoginPage = () => {
  const navigate = useNavigate();
  const qc = useQueryClient();
  const { token } = theme.useToken();
  const { mode } = useThemeMode();
  const logoSrc =
    mode === "dark"
      ? "/images/jabali_logo_dark.svg"
      : "/images/jabali_logo.svg";

  const [flow, setFlow] = useState<KratosFlow | null>(null);
  const [loadingFlow, setLoadingFlow] = useState(true);
  const [initError, setInitError] = useState<string | null>(null);

  // GH#177: Kratos is same-origin — login cookies, CSRF tokens, and allowed
  // return URLs are all bound to the panel hostname. Signing in over the raw
  // server IP can't complete the flow and surfaces a generic
  // "could not reach identity service". Detect IP-host access and steer the
  // operator to the hostname.
  const isIPHost =
    /^\d{1,3}(\.\d{1,3}){3}$/.test(window.location.hostname) ||
    window.location.hostname.includes(":");

  useEffect(() => {
    let cancelled = false;
    // Re-hydrate an existing flow when Kratos redirected here with
    // ?flow=<id> (aal2 escalation, refresh re-auth, etc.). Without
    // this, the SPA would mint a fresh aal1 flow and lose the
    // upgraded ui that Kratos prepared for us.
    const params = new URLSearchParams(window.location.search);
    const existingFlowId = params.get("flow");
    const fetcher = existingFlowId
      ? getLoginFlow(existingFlowId)
      : initLoginFlow();
    fetcher
      .then((f) => {
        if (cancelled) return;
        setFlow(f);
      })
      .catch(() => {
        if (cancelled) return;
        setInitError(
          "Could not start the sign-in flow. Check that the identity service is running and refresh the page.",
        );
      })
      .finally(() => {
        if (!cancelled) setLoadingFlow(false);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const onFinish = async (group: string, values: Record<string, unknown>) => {
    if (!flow) return;
    const body: Record<string, string> = {
      csrf_token: csrfToken(flow),
      method: group,
    };
    for (const [k, v] of Object.entries(values)) {
      body[k] = v == null ? "" : String(v);
    }
    const result = await submitLoginFlow(flow, body);
    if (result.kind === "success") {
      clearIdentity();
      const me = await getIdentity();
      // Prime the AuthContext ["whoami"] cache synchronously with the
      // fresh identity before navigating. Without this, clearIdentity's
      // queryClient.invalidateQueries only schedules an async refetch:
      // by the time the admin shell mounts and RequireAdmin evaluates
      // useAuth(), the query observer can still be holding the stale
      // pre-login null — and the guard bounces the just-logged-in user
      // right back to /login. E2E run 53 captured this as the
      // "admin lands on /jabali-admin after signing in" flake that
      // failed once then passed on retry. setQueryData is a synchronous
      // cache write that notifies observers in the same tick, so the
      // next render sees the authoritative user.
      const mePrimed: MeUser | null = me
        ? {
            id: me.id,
            email: me.email,
            isAdmin: me.isAdmin,
            username: me.username,
            fullName: me.fullName,
          }
        : null;
      qc.setQueryData<MeUser | null>(["whoami"], mePrimed);
      // Honour return_to set by /profile's refresh-flow escalation
      // (M20.1). MyProfile stashes the path in sessionStorage before
      // kicking the Kratos refresh-login flow because Kratos's own
      // return_to query is dropped on the path from
      // /self-service/login/browser → /login (it survives only on the
      // flow object's request_url, which is fragile across upgrades).
      // Read + clear once so a subsequent fresh login doesn't replay
      // a stale path. Same-origin guard blocks //evil.tld phishing.
      const stashed = sessionStorage.getItem("post_login_return_to");
      sessionStorage.removeItem("post_login_return_to");
      const safeReturn =
        stashed && stashed.startsWith("/") && !stashed.startsWith("//")
          ? stashed
          : null;
      navigate(safeReturn ?? homeForRole(me?.isAdmin ?? false), { replace: true });
      return;
    }
    if (result.kind === "continue") {
      setFlow(result.flow);
      return;
    }
    if (result.kind === "redirect") {
      // Kratos's "browser_location_change_required" — most commonly
      // aal2 escalation after a successful password submit when the
      // identity has TOTP enrolled. Native navigation is required so
      // Kratos's per-flow CSRF cookie hand-off works (XHR-following
      // the redirect leaves the browser holding a stale per-flow
      // cookie that fails the next POST's csrf check).
      window.location.assign(result.url);
      return;
    }
    // Error — surface into the flow messages via a local synthetic flow so
    // the renderer path handles it uniformly.
    setFlow({
      ...flow,
      ui: {
        ...flow.ui,
        messages: [
          ...(flow.ui.messages ?? []),
          { id: 0, text: result.message, type: "error" },
        ],
      },
    });
  };

  return (
    <div
      style={{
        minHeight: "100vh",
        display: "flex",
        alignItems: "center",
        justifyContent: "center",
        padding: 16,
        background: token.colorBgLayout,
      }}
    >
      <Card style={{ width: "100%", maxWidth: 420 }}>
        <Space direction="vertical" size="large" style={{ width: "100%" }}>
          <div
            style={{
              display: "flex",
              flexDirection: "column",
              alignItems: "center",
              gap: 12,
            }}
          >
            <img
              src={logoSrc}
              alt="Jabali"
              // Prominent brand mark: scales with the viewport but is
              // capped so it never crowds the card or pushes fields below
              // the fold on small laptop/mobile screens. SVG keeps it crisp
              // on high-DPI displays at any size.
              style={{
                height: "clamp(96px, 16vw, 132px)",
                width: "auto",
                maxWidth: "100%",
              }}
            />
            <Typography.Title level={2} style={{ margin: 0 }}>
              Jabali Panel
            </Typography.Title>
          </div>

          {isIPHost && (
            <Alert
              type="warning"
              showIcon
              message="Log in using your hostname, not the IP"
              description="The identity provider is bound to the panel hostname, so signing in over the server IP address won't work. Open the panel by its hostname (the URL shown after install) and try again."
            />
          )}

          {initError && (
            <Alert message={initError} type="error" showIcon />
          )}

          {loadingFlow && !initError && (
            <div style={{ textAlign: "center", padding: 24 }}>
              <Spin />
            </div>
          )}

          {flow && <FlowForm flow={flow} onFinish={onFinish} />}
        </Space>
      </Card>
    </div>
  );
};

type FlowFormProps = {
  flow: KratosFlow;
  onFinish: (group: string, values: Record<string, unknown>) => Promise<void>;
};

/**
 * Renders each credential group in the flow as its own AntD form section.
 * Kratos puts CSRF + method-selector nodes in "default"; the other groups
 * (usually just one at a time — "password" on AAL1, "totp" on AAL2) each
 * carry their own submit button. We pick the first non-default group to
 * render as the active section; the "default" nodes are included as
 * hidden inputs on submission.
 */
function FlowForm({ flow, onFinish }: FlowFormProps) {
  const topErrors = flowMessages(flow);
  const activeGroup = pickActiveGroup(flow);

  if (!activeGroup) {
    return (
      <Alert
        message="No credential method is currently configured for your account. Contact an administrator."
        type="warning"
        showIcon
      />
    );
  }

  const fields = renderableFields(flow, activeGroup);
  return (
    <>
      {topErrors.map((msg, i) => (
        <Alert key={i} message={msg} type="error" showIcon />
      ))}
      <GroupForm
        groupName={activeGroup}
        fields={fields}
        onSubmit={(values) => onFinish(activeGroup, values)}
      />
    </>
  );
}

function pickActiveGroup(flow: KratosFlow): string | null {
  // Priority order: prefer the first non-default group that isn't one of
  // the "link existing session" helpers. In practice Kratos surfaces one
  // credential method per AAL step, so this loop returns either "password"
  // on AAL1 or the 2FA method on AAL2.
  const seen = new Set<string>();
  for (const node of flow.ui.nodes) {
    if (node.group === "default") continue;
    if (!seen.has(node.group)) seen.add(node.group);
  }
  // Preferred order if multiple groups show up at once.
  const preferred = ["totp", "lookup_secret", "password", "webauthn"];
  for (const g of preferred) {
    if (seen.has(g)) return g;
  }
  return seen.values().next().value ?? null;
}

type GroupFormProps = {
  groupName: string;
  fields: RenderableField[];
  onSubmit: (values: Record<string, unknown>) => Promise<void>;
};

function GroupForm({ groupName, fields, onSubmit }: GroupFormProps) {
  const [submitting, setSubmitting] = useState(false);

  const submit = async (values: Record<string, unknown>) => {
    setSubmitting(true);
    try {
      await onSubmit(values);
    } finally {
      setSubmitting(false);
    }
  };

  const title = groupTitle(groupName);

  return (
    <Form
      layout="vertical"
      requiredMark={false}
      onFinish={submit}
      initialValues={Object.fromEntries(fields.map((f) => [f.name, f.value]))}
    >
      {title && (
        <Typography.Paragraph style={{ marginBottom: 8 }}>
          {title}
        </Typography.Paragraph>
      )}
      {fields.map((f) => renderField(f))}
      <Form.Item style={{ marginBottom: 0 }}>
        <Button type="primary" htmlType="submit" block loading={submitting}>
          {submitLabel(groupName)}
        </Button>
      </Form.Item>
    </Form>
  );
}

function renderField(f: RenderableField) {
  if (f.kind === "hidden") {
    // Hidden inputs (csrf_token, method) are tracked via the Form's
    // initialValues (set on GroupForm). We still declare the Form.Item so
    // the field is part of the form's value snapshot on submit; no inner
    // initialValue here (Form-level initialValues is the single owner).
    return (
      <Form.Item key={f.name} name={f.name} noStyle hidden>
        <Input type="hidden" />
      </Form.Item>
    );
  }
  if (f.kind === "submit") {
    // Kratos embeds submit buttons in the node tree; we render the primary
    // submit ourselves (see GroupForm), so skip to avoid duplicates.
    return null;
  }

  const Control =
    f.kind === "password" ? (
      <Input.Password autoComplete={f.autocomplete ?? "current-password"} />
    ) : (
      <Input
        type={f.kind}
        inputMode={f.kind === "number" || f.kind === "tel" ? "numeric" : undefined}
        autoComplete={f.autocomplete}
        autoFocus={f.kind === "email" || f.kind === "text"}
      />
    );

  return (
    <Form.Item
      key={f.name}
      name={f.name}
      label={f.label ?? humanizeName(f.name)}
      rules={[{ required: f.required, message: "Required" }]}
      help={f.errors.length ? f.errors.join("; ") : undefined}
      validateStatus={f.errors.length ? "error" : undefined}
      // GH #545: operators keep entering their email here and get "invalid
      // credentials" — login is by username (the email is contact-only in the
      // identity schema). Nudge them on the identifier field.
      extra={f.name === "identifier" ? "Use your username, not your email." : undefined}
    >
      {Control}
    </Form.Item>
  );
}

function humanizeName(name: string): string {
  // "identifier" → "Identifier", "totp_code" → "Totp code", etc. Kratos
  // usually supplies a label in node.meta.label.text so this is a fallback
  // for nodes that don't.
  const spaced = name.replace(/_/g, " ");
  return spaced.charAt(0).toUpperCase() + spaced.slice(1);
}

function groupTitle(group: string): string | null {
  switch (group) {
    case "totp":
      return "Enter the 6-digit code from your authenticator app.";
    case "lookup_secret":
      return "Enter one of your backup codes.";
    case "password":
      return null; // Email + Password labels are self-explanatory.
    case "webauthn":
      return "Use your security key to sign in.";
    default:
      return null;
  }
}

function submitLabel(group: string): string {
  switch (group) {
    case "totp":
    case "lookup_secret":
      return "Verify";
    default:
      return "Sign in";
  }
}
