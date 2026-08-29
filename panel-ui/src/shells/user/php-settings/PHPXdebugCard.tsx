// PHPXdebugCard — GH #1332 item 9. Per-(user, PHP version) Xdebug toggle. Safe
// modes only (develop/coverage/profile); no remote step-debugging. Xdebug loads
// once per FPM master, so it's version-scoped, not per-domain.
import { useEffect, useState } from "react";
import { Alert, Button, Card, Select, Space, Spin, Switch, Typography } from "antd";
import { feedback } from "../../../lib/feedback";
import { apiClient } from "../../../apiClient";

type PoolRow = { php_version: string; is_default: boolean };

export function PHPXdebugCard() {
  const [versions, setVersions] = useState<PoolRow[]>([]);
  const [version, setVersion] = useState<string>("");
  const [enabled, setEnabled] = useState(false);
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
        /* no pools */
      }
    })();
  }, []);

  useEffect(() => {
    if (!version) return;
    setLoading(true);
    apiClient
      .get<{ enabled: boolean }>("/me/php-xdebug", { params: { php_version: version } })
      .then((resp) => {
        setEnabled(!!resp.data.enabled);
        setNotAllowed(false);
      })
      .catch((err) => {
        if (err?.response?.status === 403) setNotAllowed(true);
        else feedback.message.error("Failed to load Xdebug state");
      })
      .finally(() => setLoading(false));
  }, [version]);

  const save = async (next: boolean) => {
    setSaving(true);
    try {
      await apiClient.put("/me/php-xdebug", { php_version: version, enabled: next });
      setEnabled(next);
      feedback.message.success(
        next ? "Xdebug enabled — PHP restarted for this version" : "Xdebug disabled",
      );
    } catch (err) {
      const e = err as { response?: { data?: { detail?: string; error?: string } } };
      feedback.message.error(
        e.response?.data?.detail ?? e.response?.data?.error ?? "Failed to update Xdebug",
      );
    } finally {
      setSaving(false);
    }
  };

  if (notAllowed) {
    return <Alert type="info" showIcon message="Xdebug isn't enabled on your plan." />;
  }

  return (
    <Card>
      <Space direction="vertical" size="middle" style={{ width: "100%" }}>
        <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
          Turn on Xdebug for a PHP version — better error messages, stack traces,
          code coverage and a profiler (output goes to <code>~/logs</code>).
          Remote step-debugging is not enabled. Xdebug slows PHP down, so use it
          on staging, not production. Affects every site on this PHP version.
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
          <Space>
            <Switch checked={enabled} loading={saving} onChange={save} disabled={!version} />
            <Typography.Text>{enabled ? "Xdebug on" : "Xdebug off"}</Typography.Text>
          </Space>
        </Spin>
        {enabled && (
          <Button type="link" style={{ paddingInline: 0 }} disabled>
            Mode: develop, coverage, profile
          </Button>
        )}
      </Space>
    </Card>
  );
}
