// AdminSecurityUfw — M26 Step 8. Status banner, rules table (hidden
// when firewall disabled), add-rule Drawer, enable/disable buttons with
// typed-YES Modal gate.
//
// Conventions: Drawer (not Modal) for create per CONVENTIONS.md.
// Tables consume <Table.Column> children. Modal.confirm stays for the
// enable/disable typed-YES gate — that's a destructive confirmation,
// not a create form, so it's the right antd primitive.
import { useTranslation } from "react-i18next";
import {
  Alert,
  Button,
  Card,
  Drawer,
  Empty,
  Form,
  Grid,
  Input,
  message,
  Modal,
  Popconfirm,
  Select,
  Space,
  Table,
  Tag,
  Typography,
} from "antd";
import { useState } from "react";

import { DeleteOutlined } from "@icons";
import { RowActionButton } from "../../../components/RowActionButton";
import {
  useAddUfwRule,
  useDeleteUfwRule,
  useUfwStatus,
  useUfwToggle,
  type UfwAction,
  type UfwProto,
  type UfwRule,
} from "../../../hooks/useSecurityUfw";

const ACTION_OPTIONS = [
  { value: "allow", label: "allow" },
  { value: "deny", label: "deny" },
  { value: "reject", label: "reject" },
];

const PROTO_OPTIONS = [
  { value: "tcp", label: "TCP" },
  { value: "udp", label: "UDP" },
];

// Matches "22" or "1000:2000" range.
const PORT_OR_RANGE = /^\d+(:\d+)?$/;

type AddRuleFormValues = {
  action: UfwAction;
  port: string;
  proto: UfwProto;
  from?: string;
};

const renderActionTag = (a: string) => {
  const lower = a.toLowerCase();
  const color = lower.includes("allow")
    ? "green"
    : lower.includes("deny")
      ? "red"
      : "orange";
  return <Tag color={color}>{a}</Tag>;
};

export const AdminSecurityUfw = () => {
  const { t } = useTranslation();
  const status = useUfwStatus();
  const addRule = useAddUfwRule();
  const deleteRule = useDeleteUfwRule();
  const toggle = useUfwToggle();

  const [addOpen, setAddOpen] = useState(false);
  const [addForm] = Form.useForm<AddRuleFormValues>();
  const screens = Grid.useBreakpoint();
  const isDesktop = screens.lg ?? (typeof window !== "undefined" ? window.innerWidth >= 992 : true);

  const submitAdd = async (values: AddRuleFormValues) => {
    try {
      await addRule.mutateAsync({
        action: values.action,
        port: values.port,
        proto: values.proto,
        // M43 Step 6: `from` is intentionally not sent. UFW UI is
        // port-policy-only. IP decisions go through CrowdSec.
      });
      message.success("Rule added");
      addForm.resetFields();
      setAddOpen(false);
    } catch (e: unknown) {
      message.error(e instanceof Error ? e.message : "Failed to add rule");
    }
  };

  const onDelete = async (rule: UfwRule) => {
    try {
      await deleteRule.mutateAsync(rule.num);
      message.success(`Removed rule #${rule.num}`);
    } catch (e: unknown) {
      message.error(e instanceof Error ? e.message : "Failed to delete rule");
    }
  };

  const openToggleModal = (enable: boolean) => {
    let typed = "";
    Modal.confirm({
      title: enable ? "Enable firewall" : "Disable firewall",
      content: (
        <Space direction="vertical" size="middle" style={{ width: "100%" }}>
          {enable ? (
            <Alert
              type="warning"
              showIcon
              message={t("adminsecurityufw.enabling_ufw_will_activate_default_deny_inco")}
            />
          ) : (
            <Alert
              type="error"
              showIcon
              message={t("adminsecurityufw.disabling_ufw_drops_host_firewall_protection")}
            />
          )}
          <Typography.Text>
            Type <Typography.Text code>YES</Typography.Text> to confirm:
          </Typography.Text>
          <Input
            placeholder={t("adminsecurityufw.yes")}
            autoComplete="off"
            onChange={(e) => {
              typed = e.target.value;
            }}
          />
        </Space>
      ),
      okText: enable ? "Enable firewall" : "Disable firewall",
      okButtonProps: { danger: !enable },
      onOk: async () => {
        if (typed !== "YES") {
          message.warning('Type "YES" exactly to confirm');
          return Promise.reject(new Error("not confirmed"));
        }
        try {
          await toggle.mutateAsync({ enable });
          message.success(enable ? "Firewall enabled" : "Firewall disabled");
        } catch (e: unknown) {
          message.error(e instanceof Error ? e.message : "Toggle failed");
          throw e;
        }
      },
    });
  };

  const active = status.data?.active ?? false;

  return (
    <Space direction="vertical" size="large" style={{ width: "100%" }}>
      {!status.isLoading && !active && (
        <Alert
          type="error"
          showIcon
          message={t("adminsecurityufw.firewall_disabled")}
          description={t("adminsecurityufw.ufw_is_not_active_rules_below_if_any_are_not")}
          action={
            <Button type="primary" onClick={() => openToggleModal(true)}>
              Enable firewall
            </Button>
          }
        />
      )}

      <Card size="small" title={t("adminsecurityufw.status")}>
        <Space wrap>
          {active ? <Tag color="green">active</Tag> : <Tag color="red">inactive</Tag>}
          {status.data?.default_in && (
            <Tag>
              default in: <strong>{status.data.default_in}</strong>
            </Tag>
          )}
          {status.data?.default_out && (
            <Tag>
              default out: <strong>{status.data.default_out}</strong>
            </Tag>
          )}
          {active ? (
            <Popconfirm
              title={t("adminsecurityufw.disable_firewall")}
              description={t("adminsecurityufw.this_drops_the_host_firewall_crowdsec_firewa")}
              okText={t("adminsecurityufw.continue")}
              okButtonProps={{ danger: true }}
              onConfirm={() => openToggleModal(false)}
            >
              <Button danger size="small">
                Disable firewall
              </Button>
            </Popconfirm>
          ) : (
            <Button type="primary" size="small" onClick={() => openToggleModal(true)}>
              Enable firewall
            </Button>
          )}
        </Space>
      </Card>

      {active ? (
        <Card
          size="small"
          title={t("adminsecurityufw.rules")}
          extra={
            <Button type="primary" size="small" onClick={() => setAddOpen(true)}>
              Add rule
            </Button>
          }
        >
          <Table<UfwRule>
            rowKey="num"
            dataSource={status.data?.rules ?? []}
            loading={status.isLoading}
            pagination={false}
            size="small"
            locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t("adminsecurityufw.no_rules")} /> }}
            scroll={{ x: "max-content" }}
          >
            <Table.Column<UfwRule> dataIndex="num" title="#" key="num" width={60} />
            <Table.Column<UfwRule>
              dataIndex="action"
              title={t("adminsecurityufw.action")}
              key="action"
              width={120}
              render={renderActionTag}
            />
            <Table.Column<UfwRule> dataIndex="to" title="To" key="to" />
            <Table.Column<UfwRule> dataIndex="from" title={t("adminsecurityufw.from")} key="from" />
            <Table.Column<UfwRule> dataIndex="proto" title={t("adminsecurityufw.proto")} key="proto" width={80} />
            <Table.Column<UfwRule>
              title=""
              key="delete"
              width={90}
              render={(_, row) => (
                <Popconfirm
                  title={t("adminsecurityufw.delete_rule")}
                  description={`Delete rule #${row.num} (${row.action} ${row.to})? Existing connections continue; new ones are subject to the next-matching rule.`}
                  okText={t("adminsecurityufw.delete")}
                  okButtonProps={{ danger: true }}
                  cancelText={t("adminsecurityufw.cancel")}
                  onConfirm={() => onDelete(row)}
                >
                  <RowActionButton danger size="small" icon={<DeleteOutlined />}>
                    Delete
                  </RowActionButton>
                </Popconfirm>
              )}
            />
          </Table>
        </Card>
      ) : (
        <Alert
          type="info"
          showIcon
          message={t("adminsecurityufw.rules_hidden_firewall_disabled")}
          description={t("adminsecurityufw.enable_the_firewall_to_view_and_manage_rules")}
        />
      )}

      <Drawer
        title={t("adminsecurityufw.add_ufw_rule")}
        open={addOpen}
        onClose={() => setAddOpen(false)}
        width={isDesktop ? 520 : undefined}
        placement="right"
        destroyOnClose
        extra={
          <Space>
            <Button onClick={() => setAddOpen(false)}>Cancel</Button>
            <Button type="primary" loading={addRule.isPending} onClick={() => addForm.submit()}>
              Add rule
            </Button>
          </Space>
        }
      >
        <Form<AddRuleFormValues>
          form={addForm}
          layout="vertical"
          onFinish={submitAdd}
          initialValues={{ action: "allow", proto: "tcp" }}
        >
          <Form.Item name="action" label={t("adminsecurityufw.action")} rules={[{ required: true }]}>
            <Select options={ACTION_OPTIONS} />
          </Form.Item>
          <Form.Item
            name="port"
            label={t("adminsecurityufw.port")}
            rules={[
              { required: true, message: "Required" },
              { pattern: PORT_OR_RANGE, message: 'Number or "lo:hi" range' },
            ]}
          >
            <Input placeholder="9999 or 1000:2000" autoComplete="off" />
          </Form.Item>
          <Form.Item name="proto" label={t("adminsecurityufw.protocol")} rules={[{ required: true }]}>
            <Select options={PROTO_OPTIONS} />
          </Form.Item>
          {/*
            M43 Step 6: per-IP UFW rules removed from the UI. CrowdSec is the
            single source of truth for IP decisions. To block an IP, use the
            CrowdSec tab → Decisions → Add (or `cscli decisions add`).
            Reference: docs/security/decision-brains.md, ADR-0089.
          */}
          <Alert
            type="info"
            showIcon
            style={{ marginTop: 8 }}
            message={t("adminsecurityufw.ufw_is_port_policy_only")}
            description={
              <span>
                IP-level decisions live in CrowdSec — open the
                <strong> CrowdSec </strong>tab to add an IP ban. UFW here
                manages port allow/deny only.
              </span>
            }
          />
        </Form>
      </Drawer>
    </Space>
  );
};
