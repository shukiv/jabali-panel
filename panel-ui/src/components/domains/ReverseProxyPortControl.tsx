// ReverseProxyPortControl — shows the local port a reverse-proxy domain
// forwards to, and changes it (GH #2544) through
// PUT /domains/:id/reverse-proxy-port. Shared by the tenant's Overview tab and
// the admin domain page. The server applies the same port rules as at create
// and answers with the reason when a port is refused.
import { useState } from "react";
import { Alert, Button, InputNumber, Modal, Space, Typography } from "antd";
import { useQueryClient } from "@tanstack/react-query";

import { apiClient } from "../../apiClient";
import { feedback } from "../../lib/feedback";

type Props = {
  domainId: string;
  domainName: string;
  port: number;
};

export const ReverseProxyPortControl = ({ domainId, domainName, port }: Props) => {
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const [next, setNext] = useState<number | null>(port);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const show = () => {
    setNext(port);
    setError(null);
    setOpen(true);
  };

  const save = async () => {
    if (next === null) {
      setError("Enter a port.");
      return;
    }
    if (next === port) {
      setOpen(false);
      return;
    }
    setSaving(true);
    setError(null);
    try {
      await apiClient.put(`/domains/${domainId}/reverse-proxy-port`, { port: next });
      feedback.message.success(`${domainName} now forwards to port ${next}. The site switches within about a minute.`);
      qc.invalidateQueries({ queryKey: ["one", "domains", domainId] });
      qc.invalidateQueries({ queryKey: ["list", "domains"] });
      setOpen(false);
    } catch (err) {
      const e = err as { response?: { data?: { message?: string; error?: string } } };
      setError(e.response?.data?.message ?? e.response?.data?.error ?? (err as Error).message);
    } finally {
      setSaving(false);
    }
  };

  return (
    <>
      <Space size="small" wrap>
        <Typography.Text>Port {port}</Typography.Text>
        <Button size="small" type="link" style={{ padding: 0 }} onClick={show}>
          Change port
        </Button>
      </Space>
      <Modal
        open={open}
        title={`Reverse proxy port for ${domainName}`}
        okText="Save"
        onOk={save}
        onCancel={() => setOpen(false)}
        confirmLoading={saving}
        destroyOnHidden
      >
        <Typography.Paragraph type="secondary">
          The site forwards to your app on 127.0.0.1 at this port. Start the app on the new
          port first: the site switches over within about a minute of saving.
        </Typography.Paragraph>
        <InputNumber
          aria-label="Port"
          autoFocus
          min={1024}
          max={65535}
          precision={0}
          value={next}
          onChange={(v) => setNext(typeof v === "number" ? v : null)}
          onPressEnter={save}
          style={{ width: 160 }}
        />
        {error ? <Alert type="error" showIcon title={error} style={{ marginTop: 12 }} /> : null}
      </Modal>
    </>
  );
};
