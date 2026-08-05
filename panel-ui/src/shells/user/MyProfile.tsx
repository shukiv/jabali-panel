// MyProfile — user-panel profile page.
//
// Post-M20 auth lives in Kratos. Password changes and TOTP enrolment
// happen via Kratos's self-service settings flow. The "Manage account
// security" button kicks the browser through /.ory/self-service/settings/browser;
// Kratos then redirects to its configured ui_url (`/settings`), which
// the SPA bounces to `/jabali-panel/profile?flow=<id>`. When this page
// sees `?flow=<id>` it fetches the flow and renders the Kratos node
// tree inline inside the Security card — no extra page, no extra tab.
import { useTranslation } from "react-i18next";
import {
  Alert,
  Button,
  Card,
  Checkbox,
  Descriptions,
  Form,
  Input,
  Modal,
  Space,
  Spin,
  Typography,
  message,
} from "antd";
import { useEffect, useMemo, useState } from "react";
import { useLocation, useNavigate } from "react-router";

import { getIdentity, type Identity } from "../../identity";
import { getActAs } from "../../impersonation";
import { passkeyEnrolFields, webauthnSupported } from "../../webauthn";
import {
  csrfToken,
  flowMessages,
  getSettingsFlow,
  initSettingsFlow,
  lookupSecretReveal,
  renderableFields,
  submitSettingsFlow,
  totpEnrolmentDisplay,
  type KratosFlow,
  type RenderableField,
} from "../../kratos";
import { MyProfileUsageCard } from "./MyProfileUsageCard";

export function MyProfile() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const location = useLocation();
  const [me, setMe] = useState<Identity | null>(null);

  // While an admin is acting as another user (#319), the Kratos session
  // cookie is still the admin's — a settings flow here would edit the ADMIN's
  // password/2FA, not the impersonated user's. Block it and explain.
  const impersonating = getActAs() !== null;
  const flowID = useMemo(() => {
    return new URLSearchParams(location.search).get("flow");
  }, [location.search]);

  const [flow, setFlow] = useState<KratosFlow | null>(null);
  const [flowLoading, setFlowLoading] = useState(false);
  const [flowError, setFlowError] = useState<string | null>(null);

  useEffect(() => {
    getIdentity().then(setMe);
  }, []);

  // Auto-start the Kratos settings flow when the page loads without a
  // ?flow= param. Use the JSON API instead of the browser flow's 303
  // dance so we can handle privileged-session refresh inline without
  // the multi-stage window.location.assign chain that kept landing
  // admins on /dashboard. initSettingsFlow returns one of three
  // states: flow ready (render), refresh required (kick a Kratos
  // login flow with refresh=true), or unauthenticated (back to login).
  useEffect(() => {
    if (flowID || impersonating) return;
    let cancelled = false;
    setFlowLoading(true);
    initSettingsFlow().then((res) => {
      if (cancelled) return;
      switch (res.kind) {
        case "flow":
          // Push the flow id into the URL so refresh / back-button
          // behaviour matches the Kratos-redirect path.
          navigate(`${location.pathname}?flow=${res.flow.id}`, { replace: true });
          break;
        case "refresh_required": {
          sessionStorage.setItem("post_login_return_to", location.pathname);
          const ret = encodeURIComponent(location.pathname);
          window.location.assign(
            `/.ory/self-service/login/browser?refresh=true&return_to=${ret}`,
          );
          break;
        }
        case "unauthenticated":
          window.location.assign("/login");
          break;
        case "error":
          setFlowError(res.message);
          setFlowLoading(false);
          break;
      }
    });
    return () => {
      cancelled = true;
    };
  }, [flowID, impersonating, location.pathname, navigate]);

  useEffect(() => {
    if (!flowID) {
      setFlow(null);
      setFlowError(null);
      return;
    }
    let cancelled = false;
    setFlowLoading(true);
    setFlowError(null);
    getSettingsFlow(flowID)
      .then((f) => {
        if (cancelled) return;
        setFlow(f);
      })
      .catch(() => {
        if (cancelled) return;
        setFlowError(
          "Could not load the security update form. The link may have expired — try Manage account security again.",
        );
      })
      .finally(() => {
        if (!cancelled) setFlowLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [flowID]);

  const onSubmit = async (group: string, values: Record<string, unknown>) => {
    if (!flow) return;
    const body: Record<string, string> = {
      csrf_token: csrfToken(flow),
      method: group,
    };
    for (const [k, v] of Object.entries(values)) {
      body[k] = v == null ? "" : String(v);
    }
    const result = await submitSettingsFlow(flow, body);
    if (result.kind === "continue") {
      setFlow(result.flow);
      const successMsg = (result.flow.ui.messages ?? []).find((m) => m.type === "success");
      if (successMsg) {
        message.success(successMsg.text);
      }
      return;
    }
    if (result.kind === "error") {
      message.error(result.message);
      return;
    }
  };

  const closeFlow = () => {
    // Strip ?flow=<id> while keeping the rest of the URL — the same
    // route mounts under both shells (/jabali-admin/profile +
    // /jabali-panel/profile) so use the current pathname rather than
    // hard-coding the user-shell path.
    navigate(location.pathname, { replace: true });
  };

  return (
    <div style={{ maxWidth: 1200, margin: "0 auto" }}>
      <Typography.Title level={2} style={{ margin: "0 0 16px" }}>
        My profile
      </Typography.Title>
      <div
        style={{
          columnWidth: 480,
          columnGap: 16,
        }}
      >
        <div style={{ breakInside: "avoid", marginBottom: 16, display: "inline-block", width: "100%" }}>
        <Card title={t("myprofile.account")} loading={!me}>
          {me && (
            <Descriptions column={1}>
              <Descriptions.Item label={t("myprofile.email")}>{me.email}</Descriptions.Item>
              {me.fullName && (
                <Descriptions.Item label={t("myprofile.full_name")}>{me.fullName}</Descriptions.Item>
              )}
              {me.username && (
                <Descriptions.Item label={t("myprofile.username")}>
                  <Typography.Text code>{me.username}</Typography.Text>
                </Descriptions.Item>
              )}
              <Descriptions.Item label={t("myprofile.user_id")}>
                <Typography.Text code>{me.id}</Typography.Text>
              </Descriptions.Item>
              <Descriptions.Item label={t("myprofile.hosting_package")}>
                {me.packageName ? (
                  me.packageName
                ) : (
                  <Typography.Text type="secondary">No package</Typography.Text>
                )}
              </Descriptions.Item>
              <Descriptions.Item label={t("myprofile.role")}>
                {me.isAdmin ? "Administrator" : "Tenant"}
              </Descriptions.Item>
              {me.createdAt && (
                <Descriptions.Item label={t("myprofile.member_since")}>
                  {new Date(me.createdAt).toLocaleDateString()}
                </Descriptions.Item>
              )}
            </Descriptions>
          )}
        </Card>
        </div>

        <div style={{ breakInside: "avoid", marginBottom: 16, display: "inline-block", width: "100%" }}>
        <Card title={t("myprofile.security")}>
          {impersonating ? (
            <Alert
              type="info"
              showIcon
              message={t("myprofile.security_settings_unavailable_while_acting_a")}
              description={t("myprofile.password_and_two_factor_2fa_totp_settings_al")}
            />
          ) : (
          <>
          {!flowID && (
            // The first useEffect above kicked window.location to the
            // Kratos browser flow — show a spinner during the round-trip.
            <div style={{ textAlign: "center", padding: 24 }}>
              <Spin />
            </div>
          )}

          {flowID && flowLoading && (
            <div style={{ textAlign: "center", padding: 24 }}>
              <Spin />
            </div>
          )}

          {flowID && flowError && (
            <Alert
              type="error"
              showIcon
              message={flowError}
              action={
                <Button size="small" type="link" onClick={closeFlow}>
                  Reset
                </Button>
              }
            />
          )}

          {flow && !flowError && (
            <SettingsFlowForms flow={flow} onSubmit={onSubmit} />
          )}
          </>
          )}
        </Card>
        </div>

        {me && (
          <div style={{ breakInside: "avoid", marginBottom: 16, display: "inline-block", width: "100%" }}>
            <MyProfileUsageCard userId={me.id} />
          </div>
        )}
      </div>
    </div>
  );
}

type SettingsFormsProps = {
  flow: KratosFlow;
  onSubmit: (group: string, values: Record<string, unknown>) => Promise<void>;
};

// Settings flows surface multiple credential groups at once (password +
// totp + lookup_secret). Render each non-default group as its own
// section so password change + 2FA enrolment live side by side.
function SettingsFlowForms({ flow, onSubmit }: SettingsFormsProps) {
  const topErrors = flowMessages(flow);
  const groups = collectGroups(flow);

  if (groups.length === 0) {
    return (
      <Alert
        type="warning"
        showIcon
        message="No editable security settings are exposed for your account."
      />
    );
  }

  return (
    <Space direction="vertical" size="middle" style={{ width: "100%" }}>
      {topErrors.map((msg, i) => (
        <Alert key={i} type="error" showIcon message={msg} />
      ))}
      {groups.map((g) => (
        <SettingsGroupForm
          key={g}
          flow={flow}
          group={g}
          onSubmit={(v) => onSubmit(g, v)}
        />
      ))}
    </Space>
  );
}

function collectGroups(flow: KratosFlow): string[] {
  const seen = new Set<string>();
  const order: string[] = [];
  for (const node of flow.ui.nodes) {
    if (node.group === "default" || node.group === "profile") continue;
    if (seen.has(node.group)) continue;
    seen.add(node.group);
    order.push(node.group);
  }
  // Stable ordering: password first, then passwordless passkeys, then 2FA methods.
  const preferred = ["password", "passkey", "totp", "lookup_secret", "webauthn"];
  return [...preferred.filter((p) => seen.has(p)), ...order.filter((g) => !preferred.includes(g))];
}

type GroupFormProps = {
  flow: KratosFlow;
  group: string;
  onSubmit: (values: Record<string, unknown>) => Promise<void>;
};

// PasskeySettingsCard — GH #917 passwordless passkey enrolment. The passkey
// settings group can't be a plain form POST: adding a passkey runs the
// WebAuthn *create* ceremony in the browser first, then submits the encoded
// credential. Removing one is a normal submit (passkey_remove=<credential id>),
// so we reuse onSubmit for that.
function PasskeySettingsCard({
  flow,
  onSubmit,
}: {
  flow: KratosFlow;
  onSubmit: (values: Record<string, unknown>) => Promise<void>;
}) {
  const [busy, setBusy] = useState(false);
  // Each registered passkey surfaces a submit node named "passkey_remove"
  // whose value is that credential's id.
  const removals = renderableFields(flow, "passkey").filter(
    (f) => f.kind === "submit" && f.name === "passkey_remove",
  );
  const supported = webauthnSupported();

  const onAdd = async () => {
    setBusy(true);
    try {
      const fields = await passkeyEnrolFields(flow);
      if (!fields) return;
      await onSubmit(fields);
      message.success("Passkey added");
    } catch {
      message.error("Passkey registration was cancelled or failed. Please try again.");
    } finally {
      setBusy(false);
    }
  };

  return (
    <Card type="inner" title="Passkeys (passwordless)" size="small">
      <Space direction="vertical" size="middle" style={{ width: "100%" }}>
        <Typography.Text type="secondary">
          Sign in without a password using Touch ID, Face ID, Windows Hello, or a
          security key. You can register more than one.
        </Typography.Text>

        {removals.length > 0 && (
          <Space direction="vertical" size="small" style={{ width: "100%" }}>
            {removals.map((f, i) => (
              <Space
                key={f.value || i}
                style={{ width: "100%", justifyContent: "space-between" }}
              >
                <Typography.Text>Passkey {i + 1}</Typography.Text>
                <Button
                  danger
                  size="small"
                  onClick={() =>
                    Modal.confirm({
                      title: "Remove this passkey?",
                      content:
                        "This device or key will no longer be able to sign you in. If it's your only passkey, make sure you still know your password.",
                      okText: "Remove",
                      okButtonProps: { danger: true },
                      cancelText: "Cancel",
                      onOk: () => onSubmit({ passkey_remove: f.value }),
                    })
                  }
                >
                  Remove
                </Button>
              </Space>
            ))}
          </Space>
        )}

        {supported ? (
          <Button type="primary" loading={busy} onClick={() => void onAdd()}>
            Add a passkey
          </Button>
        ) : (
          <Alert
            type="info"
            showIcon
            message="This browser does not support passkeys."
          />
        )}
      </Space>
    </Card>
  );
}

function SettingsGroupForm({ flow, group, onSubmit }: GroupFormProps) {
  const [form] = Form.useForm();
  const [submitting, setSubmitting] = useState(false);
  const fields = renderableFields(flow, group);
  const totpDisplay = group === "totp" ? totpEnrolmentDisplay(flow) : null;
  const recoveryCodes = group === "lookup_secret" ? lookupSecretReveal(flow) : null;

  // Passkey enrolment needs the WebAuthn create ceremony, not a plain form
  // POST — render it with its own card (Add runs navigator.credentials.create;
  // Remove reuses the normal submit path).
  if (group === "passkey") {
    return <PasskeySettingsCard flow={flow} onSubmit={onSubmit} />;
  }

  const submit = async (values: Record<string, unknown>) => {
    setSubmitting(true);
    try {
      await onSubmit(values);
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Card type="inner" title={groupTitle(group)} size="small">
      {totpDisplay && (
        <div
          style={{
            marginBottom: 16,
            padding: 12,
            background: "rgba(22, 119, 255, 0.06)",
            border: "1px solid rgba(22, 119, 255, 0.2)",
            borderRadius: 6,
            textAlign: "center",
          }}
        >
          <Typography.Text strong>
            Scan this QR with your authenticator app
          </Typography.Text>
          {totpDisplay.qrSrc && (
            <img
              src={totpDisplay.qrSrc}
              alt="TOTP QR code"
              // Responsive QR — caps at 220px on wide screens, shrinks
              // to viewport on phones. width:100%+max-width keeps it
              // square via the source image's intrinsic ratio.
              style={{
                display: "block",
                margin: "12px auto 8px",
                width: "100%",
                maxWidth: 220,
                height: "auto",
              }}
            />
          )}
          {totpDisplay.secret && (
            <Typography.Paragraph
              copyable
              style={{
                marginTop: 8,
                marginBottom: 0,
                fontFamily: "monospace",
                fontSize: 12,
                wordBreak: "break-all",
              }}
            >
              {totpDisplay.secret}
            </Typography.Paragraph>
          )}
        </div>
      )}
      {recoveryCodes && recoveryCodes.length > 0 && (
        <RecoveryCodesReveal codes={recoveryCodes} />
      )}
      <Form
        form={form}
        layout="vertical"
        requiredMark={false}
        onFinish={submit}
        initialValues={Object.fromEntries(fields.map((f) => [f.name, f.value]))}
      >
        {fields.map((f) => renderField(f))}
        {/* Submit-type Kratos nodes carry the action keyword (e.g.
            totp_unlink=true, lookup_secret_regenerate=true). Render
            one button per submit field; the button's onClick submits
            the form with that action key set. When no submit field
            exists, fall back to the generic group-default button
            (e.g. password change has only inputs, no explicit
            method-action node). */}
        {fields.filter((f) => f.kind === "submit").length > 0 ? (
          <Space wrap>
            {fields
              .filter((f) => f.kind === "submit")
              .map((f) => {
                const isDestructive =
                  f.name.endsWith("_unlink") || f.name.endsWith("_disable");
                const onPress = () =>
                  submit({ ...form.getFieldsValue(true), [f.name]: f.value });
                return (
                  <Button
                    key={f.name + "=" + f.value}
                    type="primary"
                    loading={submitting}
                    danger={isDestructive}
                    onClick={() => {
                      if (!isDestructive) {
                        onPress();
                        return;
                      }
                      Modal.confirm({
                        title: confirmTitle(f.name),
                        content: confirmBody(f.name),
                        okText: "Yes, disable",
                        okButtonProps: { danger: true },
                        cancelText: "Cancel",
                        onOk: onPress,
                      });
                    }}
                  >
                    {submitButtonLabel(group, f.name)}
                  </Button>
                );
              })}
          </Space>
        ) : (
          <Form.Item style={{ marginBottom: 0 }}>
            <Button type="primary" htmlType="submit" loading={submitting}>
              {submitLabel(group)}
            </Button>
          </Form.Item>
        )}
      </Form>
    </Card>
  );
}

function renderField(f: RenderableField) {
  if (f.kind === "hidden") {
    return (
      <Form.Item key={f.name} name={f.name} noStyle hidden>
        <Input type="hidden" />
      </Form.Item>
    );
  }
  if (f.kind === "submit") {
    return null;
  }
  const Control =
    f.kind === "password" ? (
      <Input.Password autoComplete={f.autocomplete ?? "new-password"} />
    ) : (
      <Input
        type={f.kind}
        inputMode={f.kind === "number" || f.kind === "tel" ? "numeric" : undefined}
        autoComplete={f.autocomplete}
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
    >
      {Control}
    </Form.Item>
  );
}

function humanizeName(name: string): string {
  const spaced = name.replace(/_/g, " ");
  return spaced.charAt(0).toUpperCase() + spaced.slice(1);
}

function groupTitle(group: string): string {
  switch (group) {
    case "password":
      return "Change password";
    case "totp":
      return "Two-factor (TOTP)";
    case "lookup_secret":
      return "Backup codes";
    case "webauthn":
      return "Security keys";
    default:
      return group;
  }
}

// Reveal-once block for newly-generated lookup_secret codes. Adds an
// acknowledgement checkbox + Download .txt button so the user has a
// fighting chance to actually keep them — Kratos shows them exactly
// once and a refresh / tab-close loses them forever.
function RecoveryCodesReveal({ codes }: { codes: string[] }) {
  const [acked, setAcked] = useState(false);
  const blob = codes.join("\n") + "\n";
  const downloadName = `jabali-panel-recovery-codes-${new Date().toISOString().slice(0, 10)}.txt`;
  const downloadHref = `data:text/plain;charset=utf-8,${encodeURIComponent(blob)}`;
  return (
    <Alert
      type="success"
      showIcon
      style={{ marginBottom: 16 }}
      message="Save these recovery codes — shown once"
      description={
        <Space direction="vertical" style={{ width: "100%" }}>
          <Typography.Paragraph
            copyable={{ text: blob }}
            style={{ margin: 0, fontFamily: "monospace", whiteSpace: "pre" }}
          >
            {codes.join("\n")}
          </Typography.Paragraph>
          <Space wrap>
            <Button
              type="link"
              href={downloadHref}
              download={downloadName}
              style={{ paddingLeft: 0 }}
            >
              Download .txt
            </Button>
          </Space>
          <Checkbox checked={acked} onChange={(e) => setAcked(e.target.checked)}>
            I've saved these somewhere safe
          </Checkbox>
          {!acked && (
            <Typography.Text type="warning">
              You won't see these again. If you lose your authenticator
              and don't have these, an admin must reset your 2FA.
            </Typography.Text>
          )}
        </Space>
      }
    />
  );
}

function confirmTitle(name: string): string {
  if (name === "totp_unlink") return "Disable two-factor authentication?";
  if (name === "lookup_secret_disable") return "Disable backup codes?";
  return "Disable?";
}

function confirmBody(name: string): string {
  if (name === "totp_unlink")
    return "Your authenticator entry will stop working. You'll only need your password to sign in until you re-enrol.";
  if (name === "lookup_secret_disable")
    return "Existing recovery codes will be invalidated. You'll lose your fallback if you lose your authenticator.";
  return "This action cannot be undone.";
}

function submitLabel(group: string): string {
  switch (group) {
    case "password":
      return "Update password";
    case "totp":
      return "Save TOTP";
    case "lookup_secret":
      return "Generate backup codes";
    case "webauthn":
      return "Save security key";
    default:
      return "Save";
  }
}

// Per-action button label. Kratos returns submit-type input nodes for
// the actions that need a dedicated button (totp_unlink, lookup_secret_
// regenerate, lookup_secret_disable, webauthn_remove). Localise to
// English here so the UI doesn't leak Kratos's internal field names.
function submitButtonLabel(group: string, name: string): string {
  switch (name) {
    case "totp_unlink":
      return "Disable two-factor";
    case "lookup_secret_regenerate":
      return "Regenerate backup codes";
    case "lookup_secret_disable":
      return "Disable backup codes";
    case "webauthn_remove":
      return "Remove security key";
    case "totp_code":
    case "method":
      return submitLabel(group);
    default:
      return humanizeName(name);
  }
}
