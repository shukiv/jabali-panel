// WebsiteMailCard — where the sites' PHP mail() goes (GH #2056, ADR 0174):
// the local mail server (the mail module's Stalwart) or the operator's own
// smarthost, so a server without the mail module can still send its sites'
// contact-form mail. The smarthost password is write-only: the server stores
// it sealed and only ever reports whether one is set, so the field stays empty
// and an empty field keeps the stored password. Saving with the smarthost
// selected tests it first; "Test" checks the form without saving.
import { useCallback, useEffect, useState } from "react";
import { MailOutlined } from "@icons";
import { Alert, Button, Card, Input, Radio, Select, Space, Typography } from "antd";
import { apiClient } from "../../../apiClient";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts

type Mode = "local" | "smarthost";
type TLSMode = "starttls" | "tls" | "none";

interface WebsiteMail {
  mode: Mode;
  host: string;
  port: number;
  tls: TLSMode;
  username: string;
  password_set: boolean;
  mail_module_enabled: boolean;
  allowed_ports: number[];
}

const TLS_OPTIONS: { value: TLSMode; label: string }[] = [
  { value: "starttls", label: "STARTTLS (required)" },
  { value: "tls", label: "TLS from the start" },
  { value: "none", label: "None (no login)" },
];

// errorText is the reason the panel gave, if any.
const errorText = (err: unknown, fallback: string): string => {
  const data = (err as { response?: { data?: { detail?: string } } })?.response?.data;
  if (data?.detail) return data.detail;
  return err instanceof Error && err.message ? err.message : fallback;
};

export function WebsiteMailCard() {
  const [loaded, setLoaded] = useState<WebsiteMail | null>(null);
  const [mode, setMode] = useState<Mode>("local");
  const [host, setHost] = useState("");
  const [port, setPort] = useState(587);
  const [tls, setTLS] = useState<TLSMode>("starttls");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [saving, setSaving] = useState(false);
  const [testing, setTesting] = useState(false);
  const [result, setResult] = useState<{ ok: boolean; text: string } | null>(null);

  const apply = (w: WebsiteMail) => {
    setLoaded(w);
    setMode(w.mode);
    setHost(w.host);
    setPort(w.port);
    setTLS(w.tls);
    setUsername(w.username);
    setPassword("");
  };

  const refresh = useCallback(async () => {
    try {
      const r = await apiClient.get<WebsiteMail>("/admin/settings/website-mail");
      apply(r.data);
    } catch {
      setLoaded(null);
    }
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  // No login without encryption: the server refuses it, so the form drops it.
  const loginAllowed = tls !== "none";
  const body = () => ({
    mode,
    host: host.trim(),
    port,
    tls,
    username: loginAllowed ? username.trim() : "",
    password: loginAllowed ? password : "",
  });

  const test = async () => {
    setTesting(true);
    setResult(null);
    try {
      await apiClient.post("/admin/settings/website-mail/test", body());
      setResult({ ok: true, text: "The smarthost accepted the connection" + (body().username ? " and the login." : ".") });
    } catch (e) {
      setResult({ ok: false, text: errorText(e, "The test failed.") });
    } finally {
      setTesting(false);
    }
  };

  const save = async () => {
    setSaving(true);
    setResult(null);
    try {
      const r = await apiClient.put<WebsiteMail>("/admin/settings/website-mail", body());
      apply(r.data);
      feedback.message.success(
        r.data.mode === "smarthost"
          ? "Website mail now goes through the smarthost."
          : "Website mail now goes through the local mail server.",
      );
    } catch (e) {
      const text = errorText(e, "Could not save the website mail settings.");
      setResult({ ok: false, text });
      feedback.message.error(text);
    } finally {
      setSaving(false);
    }
  };

  const showSmarthost = mode === "smarthost" || host !== "";

  return (
    <Card
      title={
        <Space>
          <MailOutlined />
          Website mail
        </Space>
      }
      style={{ marginBottom: 16 }}
      loading={loaded === null}
    >
      <Typography.Paragraph type="secondary" style={{ marginBottom: 12 }}>
        Where email sent by the websites goes: PHP mail(), which WordPress and
        most contact forms use. Use a smarthost to send through your own mail
        server, for example when this server doesn&apos;t run the mail module.
      </Typography.Paragraph>

      <Radio.Group
        value={mode}
        onChange={(e) => setMode(e.target.value as Mode)}
        style={{ display: "flex", flexDirection: "column", gap: 8, marginBottom: 12 }}
      >
        <Radio value="local">The local mail server</Radio>
        <Radio value="smarthost">A smarthost (your own mail relay)</Radio>
      </Radio.Group>

      {mode === "local" && loaded && !loaded.mail_module_enabled && (
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 12 }}
          message="The mail module is off, so websites can't send email through the local mail server. Turn on the mail module, or use a smarthost."
        />
      )}

      {showSmarthost && (
        <Space direction="vertical" size="small" style={{ width: "100%", maxWidth: 520 }}>
          <Input
            aria-label="Smarthost host"
            addonBefore="Host"
            placeholder="smtp.example.com"
            value={host}
            onChange={(e) => setHost(e.target.value)}
            autoComplete="off"
          />
          <Space wrap>
            <Select
              aria-label="Smarthost port"
              value={port}
              onChange={(v) => setPort(v)}
              style={{ width: 120 }}
              options={(loaded?.allowed_ports ?? [25, 465, 587, 2525]).map((p) => ({ value: p, label: `Port ${p}` }))}
            />
            <Select
              aria-label="Smarthost encryption"
              value={tls}
              onChange={(v) => setTLS(v)}
              style={{ width: 200 }}
              options={TLS_OPTIONS}
            />
          </Space>
          <Input
            aria-label="Smarthost username"
            addonBefore="Username"
            placeholder={loginAllowed ? "Optional" : "No login without encryption"}
            value={loginAllowed ? username : ""}
            disabled={!loginAllowed}
            onChange={(e) => setUsername(e.target.value)}
            autoComplete="off"
          />
          <Input.Password
            aria-label="Smarthost password"
            addonBefore="Password"
            placeholder={loaded?.password_set ? "Stored. Leave empty to keep it." : "Required with a username"}
            value={loginAllowed ? password : ""}
            disabled={!loginAllowed}
            onChange={(e) => setPassword(e.target.value)}
            autoComplete="new-password"
          />
        </Space>
      )}

      {result && (
        <Alert type={result.ok ? "success" : "error"} showIcon style={{ marginTop: 12 }} message={result.text} />
      )}

      <Space wrap style={{ marginTop: 12 }}>
        {showSmarthost && (
          <Button loading={testing} disabled={!host.trim()} onClick={() => void test()}>
            Test
          </Button>
        )}
        <Button type="primary" loading={saving} onClick={() => void save()}>
          Save
        </Button>
      </Space>
    </Card>
  );
}
