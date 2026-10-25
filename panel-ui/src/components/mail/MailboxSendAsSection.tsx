// MailboxSendAsSection — GH #347. Grant this mailbox permission to send email
// FROM other mailboxes' addresses in the same domain, WITHOUT receiving their
// mail. Embedded in the Edit Mailbox modal, below Forwarding/Groups.
import { useMemo, useState } from "react";
import { App, Button, Divider, List, Select, Space, Typography } from "antd";
import { DeleteOutlined, PlusOutlined } from "@icons";

import { useMailboxes } from "../../hooks/useMailboxes";
import { useSendAs, useAddSendAs, useDeleteSendAs } from "../../hooks/useSendAs";

interface MailboxSendAsSectionProps {
  mailboxId: string;
  domainId: string;
}

export function MailboxSendAsSection({ mailboxId, domainId }: MailboxSendAsSectionProps) {
  const { message } = App.useApp();
  const { data: grantors = [], isLoading } = useSendAs(mailboxId);
  const { data: candidates } = useMailboxes({ domainId });
  const addMut = useAddSendAs();
  const deleteMut = useDeleteSendAs();
  const [selected, setSelected] = useState<string | undefined>(undefined);

  // Candidate grantors: other enabled mailboxes in this domain not already granted.
  const options = useMemo(() => {
    const items = candidates?.items ?? [];
    const already = new Set(grantors.map((g) => g.grantor_mailbox_id));
    return items
      .filter((m) => m.id !== mailboxId && !m.is_disabled && !already.has(m.id))
      .map((m) => ({ label: m.email, value: m.id }));
  }, [candidates, grantors, mailboxId]);

  const add = async () => {
    if (!selected) return;
    try {
      await addMut.mutateAsync({ mailboxId, grantorMailboxId: selected });
      setSelected(undefined);
      message.success("Send-as permission added");
    } catch (err) {
      message.error(errMsg(err, "Failed to add send-as permission"));
    }
  };

  const remove = async (grantorMailboxId: string) => {
    try {
      await deleteMut.mutateAsync({ mailboxId, grantorMailboxId });
      message.success("Removed");
    } catch (err) {
      message.error(errMsg(err, "Failed to remove"));
    }
  };

  return (
    <div>
      <Divider style={{ margin: "16px 0" }} />
      <Typography.Text strong>Can send as</Typography.Text>
      <Typography.Paragraph type="secondary" style={{ fontSize: 12, marginBottom: 8 }}>
        Let this mailbox send email <em>from</em> another mailbox&apos;s address without
        receiving that mailbox&apos;s mail. The other mailbox keeps its own inbox.
      </Typography.Paragraph>
      <List
        size="small"
        loading={isLoading}
        locale={{ emptyText: "No send-as permissions" }}
        dataSource={grantors}
        renderItem={(g) => (
          <List.Item
            actions={[
              <Button
                key="del"
                type="text"
                danger
                size="small"
                icon={<DeleteOutlined />}
                onClick={() => remove(g.grantor_mailbox_id)}
              />,
            ]}
          >
            <Typography.Text style={{ fontFamily: "monospace" }}>
              {g.grantor_email}
            </Typography.Text>
          </List.Item>
        )}
      />
      <Space.Compact style={{ width: "100%", marginTop: 8 }}>
        <Select
          style={{ width: "100%" }}
          placeholder="Select a mailbox to send as"
          showSearch
          optionFilterProp="label"
          value={selected}
          onChange={(v) => setSelected(v)}
          options={options}
          notFoundContent="No eligible mailboxes in this domain"
        />
        <Button
          icon={<PlusOutlined />}
          onClick={add}
          loading={addMut.isPending}
          disabled={!selected}
        >
          Add
        </Button>
      </Space.Compact>
    </div>
  );
}

// The detail, when there is one, is the sentence for people; error is the code.
function errMsg(err: unknown, fallback: string): string {
  const data = (err as { response?: { data?: { error?: string; detail?: string } } })?.response
    ?.data;
  return data?.detail ?? data?.error ?? fallback;
}
