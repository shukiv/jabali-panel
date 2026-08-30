// CatchAllTab — M6.5 Step 2. Cross-domain catch-all overview.
//
// Lists all email-enabled domains + their catch-all target (if any).
// Create/edit dialog: pick domain, enter target mailbox email.

import { useTranslation } from "react-i18next";
import { useMemo, useState } from "react";
import { Button, Empty, Form, Modal, Select, Skeleton, Space, Table, Tag, Typography } from "antd";
import { feedback } from "../../../../lib/feedback"; // GH #970: themed toasts
import { DeleteOutlined, EditOutlined, PlusOutlined } from "@icons";
import { RowActions } from "../../../../components/RowActions";
import { useQueries, useQuery } from "@tanstack/react-query";

import { apiClient } from "../../../../apiClient";
import { useListQuery } from "../../../../hooks/useQueries";
import {
  useUpdateDomainCatchAll,
  useDeleteDomainCatchAll,
  type DomainCatchAll,
} from "../../../../hooks/useCatchAll";
import type { Domain } from "../../domains/UserDomainList";
import type { Mailbox } from "../../../../hooks/useMailboxes";

interface CatchAllRow {
  domain_id: string;
  domain_name: string;
  target: string | null;
  updated_at: string;
}

export const CatchAllTab = ({ domainId }: { domainId?: string } = {}) => {
  const { t } = useTranslation();
  const { items: domains, isLoading: loadingDomains } = useListQuery<Domain>({
    resource: "domains",
    params: { page: 1, pageSize: 200, sort: "name", order: "asc" },
  });

  const emailEnabledDomains = useMemo(
    () => domains.filter((d) => d.email_enabled && (!domainId || d.id === domainId)),
    [domains, domainId],
  );

  const results = useQueries({
    queries: emailEnabledDomains.map((d) => ({
      queryKey: ["catchall", d.id],
      queryFn: async () => {
        const { data } = await apiClient.get<DomainCatchAll>(
          `/domains/${d.id}/catchall`,
        );
        return data;
      },
    })),
  });

  const anyLoading = results.some((r) => r.isLoading);
  const rows: CatchAllRow[] = useMemo(() => {
    const out: CatchAllRow[] = [];
    for (const r of results) {
      if (r.data) out.push(r.data);
    }
    return out;
  }, [results]);

  const [editOpen, setEditOpen] = useState(false);
  const [editing, setEditing] = useState<CatchAllRow | null>(null);
  const [form] = Form.useForm<{ domain_id: string; target: string }>();

  const updateMut = useUpdateDomainCatchAll();
  const deleteMut = useDeleteDomainCatchAll();

  // #234: the catch-all target must be an existing mailbox in the chosen
  // domain — offer a picker instead of a free-text email field. Load the
  // selected domain's mailboxes; the query re-runs as domain_id changes.
  const watchedDomainID = Form.useWatch("domain_id", form);
  const { data: domainMailboxes = [], isLoading: loadingMailboxes } = useQuery({
    queryKey: ["catchall-mailboxes", watchedDomainID],
    enabled: !!watchedDomainID,
    queryFn: async () => {
      const { data } = await apiClient.get<{ data: Mailbox[] }>(
        `/domains/${watchedDomainID}/mailboxes?page=1&page_size=200&sort=local_part&order=asc`,
      );
      return data.data;
    },
  });

  const openCreate = () => {
    setEditing(null);
    form.resetFields();
    setEditOpen(true);
  };
  const openEdit = (row: CatchAllRow) => {
    setEditing(row);
    form.setFieldsValue({ domain_id: row.domain_id, target: row.target ?? "" });
    setEditOpen(true);
  };

  const submit = async () => {
    const vals = await form.validateFields();
    try {
      await updateMut.mutateAsync({ domainID: vals.domain_id, target: vals.target });
      feedback.message.success("Catch-all updated");
      setEditOpen(false);
    } catch (err) {
      const msg = (err as { response?: { data?: { error?: string } } })?.response?.data?.error
        ?? "Failed to update catch-all";
      feedback.message.error(msg);
    }
  };

  if (loadingDomains && domains.length === 0) {
    return <Skeleton active paragraph={{ rows: 4 }} />;
  }

  if (emailEnabledDomains.length === 0) {
    return <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t("catchalltab.no_email_enabled_domains_yet")} />;
  }

  // Build the mailbox options; preserve a current target that isn't in
  // the list (e.g. set before this picker existed) so editing never drops it.
  const targetOptions = domainMailboxes.map((m) => ({ label: m.email, value: m.email }));
  if (editing?.target && !targetOptions.some((o) => o.value === editing.target)) {
    targetOptions.unshift({ label: `${editing.target} (current)`, value: editing.target });
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
            Catch-All
          </Typography.Title>
          <Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>
            Set catch-all
          </Button>
        </Space>

        <Table<CatchAllRow>
          rowKey="domain_id"
          loading={anyLoading && rows.length === 0}
          dataSource={rows}
          pagination={false}
          scroll={{ x: "max-content" }}
          columns={[
            ...(domainId
              ? []
              : [
                  {
                    title: "Domain",
                    dataIndex: "domain_name",
                    sorter: (a: CatchAllRow, b: CatchAllRow) => a.domain_name.localeCompare(b.domain_name),
                  },
                ]),
            {
              title: "Target",
              dataIndex: "target",
              render: (v: string | null) =>
                v ? (
                  <Typography.Text style={{ fontFamily: "monospace" }}>{v}</Typography.Text>
                ) : (
                  <Typography.Text type="secondary">— (not set)</Typography.Text>
                ),
            },
            {
              title: "Status",
              width: 120,
              render: (_, row) =>
                row.target ? (
                  <Tag color="green">active</Tag>
                ) : (
                  <Tag>inactive</Tag>
                ),
            },
            {
              title: "Actions",
              width: 140,
              render: (_, row) => (
                <RowActions
                  actions={[
                    { key: "edit", label: "Edit target", icon: <EditOutlined />, onClick: () => openEdit(row) },
                    {
                      key: "remove",
                      label: "Remove",
                      icon: <DeleteOutlined />,
                      danger: true,
                      hidden: !row.target,
                      onClick: async () => {
                        try {
                          await deleteMut.mutateAsync(row.domain_id);
                          feedback.message.success("Catch-all cleared");
                        } catch (err) {
                          const msg = (err as { response?: { data?: { error?: string } } })?.response
                            ?.data?.error ?? "Failed to clear";
                          feedback.message.error(msg);
                        }
                      },
                      confirm: { title: `Clear catch-all for ${row.domain_name}?`, okText: "Clear" },
                    },
                  ]}
                />
              ),
            },
          ]}
        />
      </div>

      <Modal
        open={editOpen}
        title={editing ? `Catch-all: ${editing.domain_name}` : "Set catch-all"}
        onCancel={() => setEditOpen(false)}
        onOk={submit}
        okText={t("catchalltab.save")}
        confirmLoading={updateMut.isPending}
        destroyOnClose
      >
        <Form form={form} layout="vertical" preserve={false}>
          <Form.Item
            name="domain_id"
            label={t("catchalltab.domain")}
            rules={[{ required: true, message: "Select a domain" }]}
          >
            <Select
              placeholder={t("catchalltab.select_email_enabled_domain")}
              disabled={!!editing}
              onChange={() => form.setFieldValue("target", undefined)}
              options={emailEnabledDomains.map((d) => ({ label: d.name, value: d.id }))}
            />
          </Form.Item>
          <Form.Item
            name="target"
            label={t("catchalltab.target_mailbox")}
            rules={[{ required: true, message: "Select a target mailbox" }]}
            extra="Mail sent to unknown addresses at this domain is delivered to this mailbox."
          >
            <Select
              showSearch
              placeholder={watchedDomainID ? "Select a mailbox" : "Select a domain first"}
              loading={loadingMailboxes}
              disabled={!watchedDomainID}
              options={targetOptions}
              optionFilterProp="label"
              notFoundContent={loadingMailboxes ? "Loading…" : "No mailboxes in this domain"}
            />
          </Form.Item>
        </Form>
      </Modal>
    </>
  );
};
