// SharedFoldersTab — M6.5 Step 4. Cross-mailbox ACL sharing via Mailbox.shareWith.

import { useTranslation } from "react-i18next";
import { useMemo, useState } from "react";
import { Button, Checkbox, Empty, Form, Modal, Popconfirm, Select, Skeleton, Space, Tag, Tooltip, Typography } from "antd";
import { feedback } from "../../../../lib/feedback"; // GH #970: themed toasts
import { DeleteOutlined, PlusOutlined } from "@icons";
import { RowActionButton } from "../../../../components/RowActionButton";
import { useQueries } from "@tanstack/react-query";

import { apiClient } from "../../../../apiClient";
import { useListQuery } from "../../../../hooks/useQueries";
import { SearchableTableStringQ } from "../../../../components/SearchableTable";
import {
  useAllShares,
  useCreateShare,
  useDeleteShare,
  type Rights,
} from "../../../../hooks/useSharedFolders";
import type { Domain } from "../../domains/UserDomainList";

interface Mailbox {
  id: string;
  email: string;
  domain_id: string;
}

const RIGHTS_OPTS: { key: keyof Rights; label: string }[] = [
  { key: "mayRead", label: "Read" },
  { key: "mayAddItems", label: "Add" },
  { key: "mayRemoveItems", label: "Remove" },
  { key: "mayCreateChild", label: "Create folder" },
  { key: "mayRename", label: "Rename" },
  { key: "mayDelete", label: "Delete" },
  { key: "mayAdmin", label: "Admin" },
  { key: "maySubmit", label: "Submit" },
];

interface FormValues {
  owner_mailbox_id: string;
  shared_with_mailbox_id: string;
  rights: (keyof Rights)[];
}

export const SharedFoldersTab = () => {
  const { t } = useTranslation();
  const { items: domains, isLoading: loadingDomains } = useListQuery<Domain>({
    resource: "domains",
    params: { page: 1, pageSize: 200, sort: "name", order: "asc" },
  });
  const emailEnabled = useMemo(() => domains.filter((d) => d.email_enabled), [domains]);

  const mailboxResults = useQueries({
    queries: emailEnabled.map((d) => ({
      queryKey: ["list", "mailboxes", d.id, { page: 1, pageSize: 200 }],
      queryFn: async () => {
        const { data } = await apiClient.get<{ data: Mailbox[]; total: number }>(
          `/domains/${d.id}/mailboxes?page=1&page_size=200&sort=local_part&order=asc`,
        );
        return { items: data.data ?? [], domain: d };
      },
    })),
  });

  const mailboxes = useMemo(() => {
    const out: Mailbox[] = [];
    for (const r of mailboxResults) {
      if (!r.data) continue;
      out.push(...r.data.items);
    }
    return out;
  }, [mailboxResults]);

  const { data: shares = [], isLoading: sharesLoading } = useAllShares();
  const [search, setSearch] = useState("");
  const filteredShares = useMemo(() => {
    const q = search.trim().toLowerCase();
    if (!q) return shares;
    return shares.filter(
      (sh) =>
        (sh.owner_mailbox_email ?? "").toLowerCase().includes(q) ||
        (sh.shared_with_mailbox_email ?? "").toLowerCase().includes(q),
    );
  }, [shares, search]);
  const createMut = useCreateShare();
  const deleteMut = useDeleteShare();

  const [open, setOpen] = useState(false);
  const [form] = Form.useForm<FormValues>();

  const submit = async () => {
    const vals = await form.validateFields();
    const rights: Rights = {};
    for (const key of vals.rights) rights[key] = true;
    try {
      await createMut.mutateAsync({
        ownerMailboxID: vals.owner_mailbox_id,
        sharedWithMailboxID: vals.shared_with_mailbox_id,
        rights,
      });
      feedback.message.success("Share created");
      setOpen(false);
      form.resetFields();
    } catch (err) {
      const msg = (err as { response?: { data?: { error?: string } } })?.response?.data?.error
        ?? "Failed to create share";
      feedback.message.error(msg);
    }
  };

  if (loadingDomains && domains.length === 0) {
    return <Skeleton active paragraph={{ rows: 4 }} />;
  }

  if (mailboxes.length === 0) {
    return <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t("sharedfolderstab.create_mailboxes_first")} />;
  }

  return (
    <>
      <div>
        <Space
          style={{
            width: "100%",
            justifyContent: "space-between",
            marginBottom: 12,
            flexWrap: "wrap",
            rowGap: 8,
          }}
        >
          <Typography.Title level={3} style={{ margin: 0 }}>
            Shared Folders
          </Typography.Title>
          <Button type="primary" icon={<PlusOutlined />} onClick={() => setOpen(true)}>
            Share folder
          </Button>
        </Space>

        <SearchableTableStringQ
          rowKey="id"
          loading={sharesLoading}
          dataSource={filteredShares}
          searchPlaceholder="Search owner or shared-with…"
          initialSearch={search}
          onSearchChange={setSearch}
          pagination={{ defaultPageSize: 20 }}
          scroll={{ x: "max-content" }}
          columns={[
            {
              title: "Owner",
              dataIndex: "owner_mailbox_email",
              sorter: (a, b) => (a.owner_mailbox_email ?? "").localeCompare(b.owner_mailbox_email ?? ""),
              render: (v?: string) => (
                <Typography.Text style={{ fontFamily: "monospace" }}>{v ?? "—"}</Typography.Text>
              ),
            },
            {
              title: "Shared with",
              dataIndex: "shared_with_mailbox_email",
              sorter: (a, b) => (a.shared_with_mailbox_email ?? "").localeCompare(b.shared_with_mailbox_email ?? ""),
              render: (v?: string) => (
                <Typography.Text style={{ fontFamily: "monospace" }}>{v ?? "—"}</Typography.Text>
              ),
            },
            {
              title: "Rights",
              render: (_, row) => (
                <Space size={4} wrap>
                  {RIGHTS_OPTS.filter((opt) => row.rights[opt.key]).map((opt) => (
                    <Tag key={opt.key}>{opt.label}</Tag>
                  ))}
                </Space>
              ),
            },
            {
              title: "Actions",
              width: 80,
              render: (_, row) => (
                <Popconfirm
                  title={t("sharedfolderstab.remove_share")}
                  onConfirm={async () => {
                    try {
                      await deleteMut.mutateAsync({
                        ownerMailboxID: row.owner_mailbox_id,
                        shareID: row.id,
                      });
                      feedback.message.success("Share removed");
                    } catch (err) {
                      const msg = (err as { response?: { data?: { error?: string } } })?.response?.data?.error
                        ?? "Failed to remove";
                      feedback.message.error(msg);
                    }
                  }}
                  okText={t("sharedfolderstab.remove")}
                  okButtonProps={{ danger: true }}
                >
                  <Tooltip title={t("sharedfolderstab.remove")}>
                    <RowActionButton danger icon={<DeleteOutlined />}>Remove</RowActionButton>
                  </Tooltip>
                </Popconfirm>
              ),
            },
          ]}
        />
      </div>

      <Modal
        open={open}
        title={t("sharedfolderstab.share_folder")}
        onCancel={() => setOpen(false)}
        onOk={submit}
        okText={t("sharedfolderstab.share")}
        confirmLoading={createMut.isPending}
        destroyOnClose
        width={560}
      >
        <Form form={form} layout="vertical" preserve={false}>
          <Form.Item
            name="owner_mailbox_id"
            label={t("sharedfolderstab.source_mailbox_owner")}
            rules={[{ required: true }]}
          >
            <Select
              placeholder={t("sharedfolderstab.select_owner_mailbox")}
              showSearch
              optionFilterProp="label"
              options={mailboxes.map((m) => ({ label: m.email, value: m.id }))}
            />
          </Form.Item>
          <Form.Item
            name="shared_with_mailbox_id"
            label={t("sharedfolderstab.target_mailbox_grantee")}
            rules={[{ required: true }]}
          >
            <Select
              placeholder={t("sharedfolderstab.select_target_mailbox")}
              showSearch
              optionFilterProp="label"
              options={mailboxes.map((m) => ({ label: m.email, value: m.id }))}
            />
          </Form.Item>
          <Form.Item
            name="rights"
            label={t("sharedfolderstab.rights")}
            initialValue={["mayRead"]}
            rules={[{ required: true, message: "Select at least one right" }]}
          >
            <Checkbox.Group
              options={RIGHTS_OPTS.map((o) => ({ label: o.label, value: o.key }))}
            />
          </Form.Item>
        </Form>
      </Modal>
    </>
  );
};
