// PHPExtensionsCard — GH #1332 item 16 (+ follow-up: reflect ACTUAL state).
// Per-(user, PHP version) extensions. Extensions load once per FPM master (one
// per version), so this is version-scoped. The card mirrors the real per-version
// state: server-default modules show as "always on" (read-only — a tenant can't
// disable a base extension for one pool), Xdebug (its own control) is reflected
// when on, and installed extras the tenant may opt into are the only toggles.
import { useEffect, useState } from "react";
import {
  Alert,
  Button,
  Card,
  Checkbox,
  Empty,
  Select,
  Space,
  Spin,
  Typography,
} from "antd";
import { feedback } from "../../../lib/feedback";
import { apiClient } from "../../../apiClient";

type PoolRow = { php_version: string; is_default: boolean };
type ExtResp = {
  php_version: string;
  available: string[];
  enabled: string[];
  // always_on is the version's server-default-enabled set (conf.d, built-ins
  // included). Absent when the agent couldn't be reached — then we hide the
  // always-on group rather than claim those modules are off.
  always_on?: string[];
  xdebug_on?: boolean;
};

export function PHPExtensionsCard() {
  const [versions, setVersions] = useState<PoolRow[]>([]);
  const [version, setVersion] = useState<string>("");
  const [available, setAvailable] = useState<string[]>([]);
  const [enabled, setEnabled] = useState<string[]>([]);
  const [alwaysOn, setAlwaysOn] = useState<string[] | null>(null);
  const [xdebugOn, setXdebugOn] = useState(false);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [notAllowed, setNotAllowed] = useState(false);

  useEffect(() => {
    (async () => {
      try {
        const resp = await apiClient.get<{ pools: PoolRow[] }>("/me/php-pool-tuning");
        const pools = resp.data?.pools ?? [];
        setVersions(pools);
        const def = pools.find((p) => p.is_default) ?? pools[0];
        if (def) setVersion(def.php_version);
      } catch {
        /* no pools -> nothing to configure */
      }
    })();
  }, []);

  useEffect(() => {
    if (!version) return;
    setLoading(true);
    apiClient
      .get<ExtResp>("/me/php-extensions", { params: { php_version: version } })
      .then((resp) => {
        setAvailable(resp.data.available ?? []);
        setEnabled(resp.data.enabled ?? []);
        // undefined (agent down) -> null so the always-on group is hidden.
        setAlwaysOn(resp.data.always_on ?? null);
        setXdebugOn(!!resp.data.xdebug_on);
        setNotAllowed(false);
      })
      .catch((err) => {
        if (err?.response?.status === 403) setNotAllowed(true);
        else feedback.message.error("Failed to load extensions");
      })
      .finally(() => setLoading(false));
  }, [version]);

  const save = async () => {
    setSaving(true);
    try {
      await apiClient.put("/me/php-extensions", {
        php_version: version,
        extensions: enabled,
      });
      feedback.message.success(
        "Extensions updated — PHP restarted for this version",
      );
    } catch (err) {
      const e = err as { response?: { data?: { detail?: string; error?: string } } };
      feedback.message.error(
        e.response?.data?.detail ?? e.response?.data?.error ?? "Failed to save",
      );
    } finally {
      setSaving(false);
    }
  };

  if (notAllowed) {
    return (
      <Alert
        type="info"
        showIcon
        message="Extension management isn't enabled on your plan."
      />
    );
  }

  // Toggles are the extras the tenant may opt into: installed, not a server
  // default, and not Xdebug (managed by its own control). Xdebug and server
  // defaults show read-only in the always-on group below.
  const alwaysOnSet = new Set(alwaysOn ?? []);
  const toggles = available.filter((a) => !alwaysOnSet.has(a) && a !== "xdebug");
  const toggleValue = enabled.filter((e) => toggles.includes(e));

  // Read-only "always on" rows: the server defaults, plus Xdebug when its own
  // control has it on. Only shown when the agent reported the real state.
  const alwaysOnRows =
    alwaysOn === null
      ? []
      : [...alwaysOn, ...(xdebugOn ? ["xdebug"] : [])];

  const gridStyle = {
    display: "grid",
    gridTemplateColumns: "repeat(auto-fill, minmax(140px, 1fr))",
    gap: 8,
  } as const;

  return (
    <Card>
      <Space direction="vertical" size="middle" style={{ width: "100%" }}>
        <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
          Turn on additional PHP extensions for a version. This affects every
          site you run on that PHP version (extensions load once per pool).
          Server-default and always-on extensions are shown for reference and
          can't be turned off per domain.
        </Typography.Paragraph>

        {versions.length > 1 && (
          <Select
            style={{ minWidth: 220 }}
            value={version}
            onChange={setVersion}
            options={versions.map((p) => ({
              value: p.php_version,
              label: `PHP ${p.php_version}${p.is_default ? " (default)" : ""}`,
            }))}
          />
        )}

        <Spin spinning={loading}>
          {alwaysOnRows.length > 0 && (
            <div style={{ marginBottom: 16 }}>
              <Typography.Text strong>Always on (server default)</Typography.Text>
              <Typography.Paragraph type="secondary" style={{ fontSize: 12, margin: "2px 0 8px" }}>
                Enabled for every site on PHP {version}.
                {xdebugOn ? " Xdebug is managed in the Xdebug control above." : ""}
              </Typography.Paragraph>
              <Checkbox.Group
                value={alwaysOnRows}
                disabled
                style={gridStyle}
                options={alwaysOnRows.map((name) => ({
                  label: name === "xdebug" ? "xdebug (Xdebug control)" : name,
                  value: name,
                }))}
              />
            </div>
          )}

          <Typography.Text strong>Optional extras</Typography.Text>
          {toggles.length === 0 ? (
            <Empty
              image={Empty.PRESENTED_IMAGE_SIMPLE}
              description="No optional extensions available for this version."
              style={{ margin: "8px 0" }}
            />
          ) : (
            <Checkbox.Group
              value={toggleValue}
              onChange={(v) => setEnabled(v as string[])}
              style={{ ...gridStyle, marginTop: 8 }}
              options={toggles.map((name) => ({ label: name, value: name }))}
            />
          )}
          <div style={{ marginTop: 16 }}>
            <Button
              type="primary"
              loading={saving}
              disabled={!version || toggles.length === 0}
              onClick={save}
            >
              Save extensions
            </Button>
          </div>
        </Spin>
      </Space>
    </Card>
  );
}
