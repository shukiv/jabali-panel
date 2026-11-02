// DomainBotExclusionSection — GH #2328. Paths of this domain that skip the
// AppSec bot-detection challenge, for clients that cannot solve it (an API, a
// webhook, a mobile app). Admin only. Same table + inline add form as the
// IP allow/deny section beside it.
import { useState } from "react";
import { Button, Form, Input, Popconfirm, Select, Skeleton, Space, Table, Tag, Typography } from "antd";
import { feedback } from "../../../lib/feedback";
import { DeleteOutlined, PlusOutlined } from "@icons";

import {
  BOT_EXCLUSION_METHODS,
  useCreateDomainBotExclusion,
  useDeleteDomainBotExclusion,
  useDomainBotExclusions,
  type CreateBotExclusionInput,
  type DomainBotExclusion,
} from "../../../hooks/useDomainBotExclusions";
import { CollapsibleAlert } from "../../../components/CollapsibleAlert";

type Props = { domainId: string; domainName: string };

const METHOD_OPTIONS = BOT_EXCLUSION_METHODS.map((m) => ({ value: m, label: m }));

export const DomainBotExclusionSection = ({ domainId, domainName }: Props) => {
  const { data, isLoading } = useDomainBotExclusions(domainId);
  const create = useCreateDomainBotExclusion(domainId);
  const remove = useDeleteDomainBotExclusion(domainId);
  const [form] = Form.useForm<CreateBotExclusionInput>();
  const [adding, setAdding] = useState(false);

  if (isLoading && !data) {
    return <Skeleton active paragraph={{ rows: 3 }} />;
  }

  const rows = data?.data ?? [];

  const onAdd = async (values: CreateBotExclusionInput) => {
    try {
      await create.mutateAsync({
        path_prefix: values.path_prefix.trim(),
        methods: values.methods ?? [],
        note: values.note ?? "",
      });
      feedback.message.success("Path added");
      form.resetFields();
      setAdding(false);
    } catch (err: unknown) {
      const resp = (err as { response?: { data?: { error?: string } } })
        ?.response?.data;
      feedback.message.error(resp?.error ?? "Failed to add path");
    }
  };

  const onDelete = async (id: string) => {
    try {
      await remove.mutateAsync({ id });
      feedback.message.success("Path removed");
    } catch {
      feedback.message.error("Failed to remove path");
    }
  };

  return (
    <Space direction="vertical" style={{ width: "100%" }} size="middle">
      <CollapsibleAlert
        storageKey="domain.bot-exclusions"
        type="info"
        showIcon
        message="Requests to these paths skip the bot-detection challenge"
        description={
          <Typography.Paragraph style={{ marginBottom: 0 }}>
            For clients that cannot solve the challenge, such as an API, a
            webhook or a mobile app. Applies to {domainName} and www.
            {domainName}, and only while bot detection is on (Security →
            CrowdSec → Bot Detection). A path matches as a prefix, without the
            query string: <code>/api/</code> also covers{" "}
            <code>/api/orders?id=1</code>. The WAF, rate limits and IP bans
            still apply. Changes take effect in about a minute.
          </Typography.Paragraph>
        }
      />

      <Table<DomainBotExclusion>
        dataSource={rows}
        rowKey="id"
        pagination={false}
        size="small"
        scroll={{ x: "max-content" }}
        locale={{ emptyText: "No paths. Every path gets the challenge." }}
      >
        <Table.Column<DomainBotExclusion>
          title="Path"
          dataIndex="path_prefix"
          render={(p: string) => <Typography.Text code>{p}</Typography.Text>}
        />
        <Table.Column<DomainBotExclusion>
          title="Methods"
          dataIndex="methods"
          render={(ms: string[]) =>
            ms.length === 0 ? (
              <Typography.Text type="secondary">Any</Typography.Text>
            ) : (
              ms.map((m) => <Tag key={m}>{m}</Tag>)
            )
          }
        />
        <Table.Column<DomainBotExclusion>
          title="Note"
          dataIndex="note"
          render={(n: string) =>
            n ? (
              <Typography.Text>{n}</Typography.Text>
            ) : (
              <Typography.Text type="secondary">—</Typography.Text>
            )
          }
        />
        <Table.Column<DomainBotExclusion>
          title=""
          width={80}
          render={(_, r) => (
            <Popconfirm
              title="Remove this path?"
              onConfirm={() => onDelete(r.id)}
              okText="Remove"
              okButtonProps={{ danger: true }}
            >
              <Button
                size="small"
                type="primary"
                danger
                aria-label={`Remove ${r.path_prefix}`}
                icon={<DeleteOutlined />}
              />
            </Popconfirm>
          )}
        />
      </Table>

      {adding ? (
        <Form<CreateBotExclusionInput> form={form} layout="inline" onFinish={onAdd}>
          <Form.Item
            label="Path"
            name="path_prefix"
            rules={[
              { required: true, message: "Required" },
              { pattern: /^\s*\//, message: "Starts with /" },
            ]}
          >
            <Input placeholder="/api/webhook" style={{ width: 220 }} />
          </Form.Item>
          <Form.Item label="Methods" name="methods">
            <Select
              mode="multiple"
              allowClear
              options={METHOD_OPTIONS}
              placeholder="Any"
              style={{ minWidth: 160 }}
            />
          </Form.Item>
          <Form.Item label="Note" name="note">
            <Input style={{ width: 200 }} />
          </Form.Item>
          <Form.Item>
            <Space>
              <Button type="primary" htmlType="submit" loading={create.isPending}>
                Add
              </Button>
              <Button
                onClick={() => {
                  form.resetFields();
                  setAdding(false);
                }}
              >
                Cancel
              </Button>
            </Space>
          </Form.Item>
        </Form>
      ) : (
        <Button icon={<PlusOutlined />} onClick={() => setAdding(true)}>
          Add path
        </Button>
      )}
    </Space>
  );
};
