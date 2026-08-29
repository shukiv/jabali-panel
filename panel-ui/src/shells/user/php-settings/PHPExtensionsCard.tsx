// PHPExtensionsCard — GH #1332 item 16. Per-(user, PHP version) opt-in extra
// extensions. Extensions load once per FPM master (one per version), so this is
// version-scoped: a tenant can turn ON installed extras for their pool; base
// extensions stay enabled server-wide.
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
type ExtResp = { php_version: string; available: string[]; enabled: string[] };

export function PHPExtensionsCard() {
  const [versions, setVersions] = useState<PoolRow[]>([]);
  const [version, setVersion] = useState<string>("");
  const [available, setAvailable] = useState<string[]>([]);
  const [enabled, setEnabled] = useState<string[]>([]);
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

  return (
    <Card>
      <Space direction="vertical" size="middle" style={{ width: "100%" }}>
        <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
          Turn on additional PHP extensions for a version. This affects every
          site you run on that PHP version (extensions load once per pool). You
          can add installed extras; base extensions are always on.
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
          {available.length === 0 ? (
            <Empty description="No optional extensions available." />
          ) : (
            <Checkbox.Group
              value={enabled}
              onChange={(v) => setEnabled(v as string[])}
              style={{ display: "grid", gridTemplateColumns: "repeat(auto-fill, minmax(140px, 1fr))", gap: 8 }}
              options={available.map((name) => ({ label: name, value: name }))}
            />
          )}
          <div style={{ marginTop: 16 }}>
            <Button type="primary" loading={saving} disabled={!version} onClick={save}>
              Save extensions
            </Button>
          </div>
        </Spin>
      </Space>
    </Card>
  );
}
