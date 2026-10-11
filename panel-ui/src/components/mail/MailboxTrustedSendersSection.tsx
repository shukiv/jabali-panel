// MailboxTrustedSendersSection — GH #2017. Senders this mailbox trusts: their
// mail is not treated as spam when it passes SPF or DMARC. The Trusted senders
// tab of the Edit mailbox drawer.
import { useState } from "react";
import { App, Button, Input, List, Space, Typography } from "antd";
import { DeleteOutlined, PlusOutlined } from "@icons";

import {
  useAddTrustedSender,
  useDeleteTrustedSender,
  useTrustedSenders,
} from "../../hooks/useTrustedSenders";

interface MailboxTrustedSendersSectionProps {
  mailboxId: string;
}

export function MailboxTrustedSendersSection({ mailboxId }: MailboxTrustedSendersSectionProps) {
  const { message } = App.useApp();
  const { data, isLoading } = useTrustedSenders(mailboxId);
  const addMut = useAddTrustedSender();
  const deleteMut = useDeleteTrustedSender();
  const [address, setAddress] = useState("");

  const senders = data?.data ?? [];
  const total = data?.total ?? senders.length;
  const max = data?.max ?? 500;
  const typed = address.trim();
  const full = total >= max;

  const add = async () => {
    if (!typed || full) return;
    try {
      const added = await addMut.mutateAsync({ mailboxId, address: typed });
      setAddress("");
      if (added.warning) {
        message.warning(added.warning);
      } else {
        message.success(`${added.address} is trusted`);
      }
    } catch (err) {
      const res = (err as { response?: { status?: number; data?: { error?: string; detail?: string } } }).response;
      if (res?.status === 409) {
        message.error("This sender is already trusted");
        return;
      }
      message.error(res?.data?.detail ?? res?.data?.error ?? "Could not add the sender");
    }
  };

  const remove = async (id: string) => {
    try {
      await deleteMut.mutateAsync({ mailboxId, id });
      message.success("Removed");
    } catch (err) {
      const res = (err as { response?: { data?: { error?: string; detail?: string } } }).response;
      message.error(res?.data?.detail ?? res?.data?.error ?? "Could not remove the sender");
    }
  };

  return (
    <div>
      <Typography.Paragraph type="secondary" style={{ fontSize: 12, marginBottom: 8 }}>
        Mail from these senders is not treated as spam, as long as it passes SPF or DMARC: a forged
        sender is still filtered. The address must match exactly, ignoring case. A +tag address,
        like name+news@example.com, is a different sender. Contacts saved in webmail are trusted
        the same way.
      </Typography.Paragraph>
      <Space.Compact style={{ width: "100%" }}>
        <Input
          aria-label="Sender address"
          placeholder="name@example.com"
          value={address}
          onChange={(e) => setAddress(e.target.value)}
          onPressEnter={add}
          autoComplete="off"
        />
        <Button icon={<PlusOutlined />} onClick={add} loading={addMut.isPending} disabled={!typed || full}>
          Add
        </Button>
      </Space.Compact>
      <Typography.Text type="secondary" style={{ display: "block", fontSize: 12, marginTop: 4 }}>
        {`${total} of ${max}`}
      </Typography.Text>
      <List
        size="small"
        loading={isLoading}
        locale={{ emptyText: "No trusted senders" }}
        dataSource={senders}
        renderItem={(s) => (
          <List.Item
            actions={[
              <Button
                key="del"
                type="text"
                danger
                size="small"
                icon={<DeleteOutlined />}
                aria-label={`Remove ${s.address}`}
                onClick={() => remove(s.id)}
              />,
            ]}
          >
            <Typography.Text style={{ fontFamily: "monospace", wordBreak: "break-all" }}>{s.address}</Typography.Text>
          </List.Item>
        )}
      />
    </div>
  );
}
