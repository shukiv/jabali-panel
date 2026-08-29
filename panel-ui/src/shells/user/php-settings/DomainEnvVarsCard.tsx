// DomainEnvVarsCard — GH #1332 item 14. Per-domain environment variables,
// delivered to PHP as fastcgi_param (reach getenv() + $_SERVER). Self-contained
// (its own domain picker) so it drops into the PHP Settings tabs cleanly.
import { useEffect, useState } from "react";
import {
  Alert,
  Button,
  Card,
  Input,
  Select,
  Space,
  Spin,
  Typography,
} from "antd";
import { DeleteOutlined, PlusOutlined } from "@ant-design/icons";
import { feedback } from "../../../lib/feedback";
import { apiClient } from "../../../apiClient";

type Domain = { id: string; name: string };
type EnvVar = { key: string; value: string };

// Mirrors the server denylist (phpenv) so the user gets an inline hint before
// submitting; the API + agent are the real enforcement.
const RESERVED = new Set([
  "PHP_VALUE",
  "PHP_ADMIN_VALUE",
  "PHP_ADMIN_FLAG",
  "SCRIPT_FILENAME",
  "DOCUMENT_ROOT",
  "PATH_INFO",
  "PATH_TRANSLATED",
  "QUERY_STRING",
  "HTTPS",
  "REDIRECT_STATUS",
]);
const KEY_RE = /^[A-Za-z_][A-Za-z0-9_]{0,63}$/;

function keyError(key: string): string | null {
  if (key === "") return null;
  if (!KEY_RE.test(key)) return "Letters, digits, underscore; can't start with a digit";
  if (RESERVED.has(key.toUpperCase()) || key.toUpperCase().startsWith("HTTP_"))
    return "Reserved name";
  return null;
}

export function DomainEnvVarsCard() {
  const [domains, setDomains] = useState<Domain[]>([]);
  const [selectedDomain, setSelectedDomain] = useState<string | null>(null);
  const [rows, setRows] = useState<EnvVar[]>([]);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    (async () => {
      try {
        const resp = await apiClient.get<{ data: Domain[] }>("/domains");
        setDomains(resp.data?.data ?? []);
      } catch {
        feedback.message.error("Failed to load domains");
      }
    })();
  }, []);

  useEffect(() => {
    if (!selectedDomain) {
      setRows([]);
      return;
    }
    setLoading(true);
    apiClient
      .get<{ env_vars: EnvVar[] }>(`/domains/${selectedDomain}/env-vars`)
      .then((resp) => setRows(resp.data.env_vars ?? []))
      .catch(() => feedback.message.error("Failed to load environment variables"))
      .finally(() => setLoading(false));
  }, [selectedDomain]);

  const setRow = (i: number, patch: Partial<EnvVar>) =>
    setRows((prev) => prev.map((r, idx) => (idx === i ? { ...r, ...patch } : r)));
  const addRow = () => setRows((prev) => [...prev, { key: "", value: "" }]);
  const removeRow = (i: number) =>
    setRows((prev) => prev.filter((_, idx) => idx !== i));

  const dupKeys = (() => {
    const seen = new Set<string>();
    const dups = new Set<string>();
    for (const r of rows) {
      if (r.key && seen.has(r.key)) dups.add(r.key);
      seen.add(r.key);
    }
    return dups;
  })();

  const hasErrors =
    rows.some((r) => r.key.trim() === "" || keyError(r.key) !== null) ||
    dupKeys.size > 0;

  const save = async () => {
    if (!selectedDomain) return;
    setSaving(true);
    try {
      await apiClient.put(`/domains/${selectedDomain}/env-vars`, {
        env_vars: rows.map((r) => ({ key: r.key, value: r.value })),
      });
      feedback.message.success("Environment variables saved");
    } catch (err) {
      const e = err as { response?: { data?: { detail?: string; error?: string } } };
      feedback.message.error(
        e.response?.data?.detail ?? e.response?.data?.error ?? "Failed to save",
      );
    } finally {
      setSaving(false);
    }
  };

  return (
    <Card>
      <Space direction="vertical" size="middle" style={{ width: "100%" }}>
        <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
          Per-domain environment variables for PHP apps. They're delivered to
          PHP&nbsp;via <code>getenv()</code> and <code>$_SERVER</code> (works for
          Laravel/Symfony <code>env()</code>). They apply only to this
          domain&apos;s PHP requests.
        </Typography.Paragraph>

        <Select
          style={{ minWidth: 280 }}
          placeholder="Select a domain"
          value={selectedDomain}
          onChange={setSelectedDomain}
          options={domains.map((d) => ({ label: d.name, value: d.id }))}
        />

        {selectedDomain && (
          <Spin spinning={loading}>
            <Space direction="vertical" size="small" style={{ width: "100%" }}>
              {rows.length === 0 && (
                <Typography.Text type="secondary">
                  No variables yet.
                </Typography.Text>
              )}
              {rows.map((r, i) => {
                const err = keyError(r.key) ?? (dupKeys.has(r.key) ? "Duplicate" : null);
                return (
                  <Space
                    key={i}
                    align="start"
                    style={{ width: "100%" }}
                    wrap
                  >
                    <div>
                      <Input
                        placeholder="KEY"
                        value={r.key}
                        status={err ? "error" : undefined}
                        style={{ width: 200 }}
                        onChange={(e) => setRow(i, { key: e.target.value })}
                      />
                      {err && (
                        <Typography.Text
                          type="danger"
                          style={{ fontSize: 11, display: "block" }}
                        >
                          {err}
                        </Typography.Text>
                      )}
                    </div>
                    <Input
                      placeholder="value"
                      value={r.value}
                      style={{ width: 320 }}
                      onChange={(e) => setRow(i, { value: e.target.value })}
                    />
                    <Button
                      icon={<DeleteOutlined />}
                      onClick={() => removeRow(i)}
                      aria-label="Remove variable"
                    />
                  </Space>
                );
              })}
              <Button icon={<PlusOutlined />} onClick={addRow}>
                Add variable
              </Button>
              {hasErrors && (
                <Alert
                  type="warning"
                  showIcon
                  message="Fix the highlighted keys before saving (no empty, reserved, or duplicate names)."
                />
              )}
              <Button
                type="primary"
                loading={saving}
                disabled={hasErrors}
                onClick={save}
              >
                Save environment variables
              </Button>
            </Space>
          </Spin>
        )}
      </Space>
    </Card>
  );
}
