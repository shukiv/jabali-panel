import { useEffect, useState } from "react";
import { Alert, Button, Card, Form, Input, Space, Tag, Typography, notification } from "antd";

import { apiClient } from "../../../apiClient";

// FreeHostnameCard — Server Settings → General. Activate a free
// <ip>.jabalihosted.com hostname on an already-installed box (JAB-213): the
// panel emails a 6-digit code to verify the address, then claims a hostname
// derived from this box's public IP + switches the panel to it. cPanel-cprapid
// equivalent. The manual-hostname path is unaffected; this is purely additive.

type Step = "idle" | "code_sent";

interface Settings {
  hostname?: string;
}

export const FreeHostnameCard = () => {
  const [hostname, setHostname] = useState<string>("");
  const [step, setStep] = useState<Step>("idle");
  const [email, setEmail] = useState<string>("");
  const [code, setCode] = useState<string>("");
  const [sending, setSending] = useState(false);
  const [claiming, setClaiming] = useState(false);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const resp = await apiClient.get<Settings>("/admin/settings");
        if (!cancelled) setHostname(resp.data.hostname ?? "");
      } catch {
        /* non-fatal for this card */
      }
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  const active = hostname.endsWith(".jabalihosted.com");

  const sendCode = async () => {
    setSending(true);
    try {
      await apiClient.post("/admin/settings/free-hostname/register", { email: email.trim() });
      setStep("code_sent");
      notification.success({ message: `Verification code sent to ${email.trim()}` });
    } catch (e: unknown) {
      notification.error({ message: errText(e, "Could not send the code") });
    } finally {
      setSending(false);
    }
  };

  const claim = async () => {
    setClaiming(true);
    try {
      const resp = await apiClient.post<{ fqdn: string }>("/admin/settings/free-hostname/claim", {
        email: email.trim(),
        code: code.trim(),
      });
      setHostname(resp.data.fqdn);
      setStep("idle");
      setCode("");
      notification.success({
        message: `Hostname activated: ${resp.data.fqdn}`,
        description:
          "DNS + TLS are provisioning. The panel URL will change to the new hostname shortly.",
      });
    } catch (e: unknown) {
      notification.error({ message: errText(e, "Could not claim the hostname") });
    } finally {
      setClaiming(false);
    }
  };

  return (
    <Card
      title={
        <>
          Free Jabali hostname {active && <Tag color="green">active</Tag>}
        </>
      }
      style={{ marginBottom: 16 }}
    >
      <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
        Get a public hostname like <code>203-0-113-7.jabalihosted.com</code> with automatic
        DNS + TLS, derived from this server's public IP. We email a one-time code to verify
        the address (stored only for contact about the hostname).
      </Typography.Paragraph>

      {active ? (
        <Alert
          type="success"
          showIcon
          message={`This panel uses ${hostname}`}
          description="DNS and certificates are managed automatically. To switch to your own domain, set the hostname on the General tab."
        />
      ) : (
        <Form layout="vertical" style={{ maxWidth: 420 }}>
          <Form.Item label="Email">
            <Input
              type="email"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              placeholder="you@example.com"
              disabled={step === "code_sent"}
            />
          </Form.Item>
          {step === "idle" ? (
            <Button type="primary" loading={sending} disabled={!email.includes("@")} onClick={sendCode}>
              Send verification code
            </Button>
          ) : (
            <Space direction="vertical" style={{ width: "100%" }}>
              <Form.Item label="6-digit code" style={{ marginBottom: 8 }}>
                <Input
                  value={code}
                  onChange={(e) => setCode(e.target.value.replace(/[^0-9]/g, ""))}
                  maxLength={6}
                  placeholder="123456"
                />
              </Form.Item>
              <Space>
                <Button type="primary" loading={claiming} disabled={code.length !== 6} onClick={claim}>
                  Activate hostname
                </Button>
                <Button type="link" onClick={() => setStep("idle")}>
                  Use a different email
                </Button>
              </Space>
            </Space>
          )}
        </Form>
      )}
    </Card>
  );
};

function errText(e: unknown, fallback: string): string {
  const anyErr = e as { response?: { data?: { message?: string; error?: string } } };
  return anyErr?.response?.data?.message || anyErr?.response?.data?.error || fallback;
}
