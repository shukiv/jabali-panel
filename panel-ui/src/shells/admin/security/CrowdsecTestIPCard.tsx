// CrowdsecTestIPCard — "would this IP be blocked?" form. Folded into
// the CrowdSec tab from the deleted M43 Trust sub-tab. POSTs to
// /admin/security/trust/test (route preserved; renaming would break
// the endpoint without value).
//
// When the crowdsec layer returns "deny" on the operator's own IP
// (common after they fat-finger an SSH password and trip ssh-bf),
// the table renders an inline Unban button that DELETEs every
// active decision for that IP/CIDR. Lets the operator self-recover
// without dropping to a shell.
import { useTranslation } from "react-i18next";
import { Alert, App, Button, Card, Form, Input, Popconfirm, Space, Table, Tag, Typography } from "antd";

import { useTrustTest, useUnbanIP, type TrustTestResponse, type TrustVerdict } from "../../../hooks/useSecurityTrust";

export const CrowdsecTestIPCard = () => {
  const { t } = useTranslation();
  const trustTest = useTrustTest();
  const unbanIP = useUnbanIP();
  const { message } = App.useApp();
  const [form] = Form.useForm<{ ip: string }>();
  const lastResult: TrustTestResponse | undefined = trustTest.data;
  const onTest = ({ ip }: { ip: string }) => trustTest.mutate(ip.trim());
  const handleUnban = (ip: string) => {
    unbanIP.mutate(ip, {
      onSuccess: () => {
        message.success(`Unbanned ${ip}`);
        // Re-test so the table reflects the fresh state.
        trustTest.mutate(ip);
      },
      onError: (err) =>
        message.error(err instanceof Error ? err.message : "Unban failed"),
    });
  };

  return (
    <Card size="small" title={t("crowdsectestipcard.test_ip_would_this_ip_be_blocked")}>
      <Typography.Paragraph type="secondary" style={{ marginBottom: 12 }}>
        Asks every brain at once. Returns each layer's verdict so you can
        spot disagreement (the failure mode M43 fixes — see ADR-0089).
      </Typography.Paragraph>
      <Form form={form} layout="inline" onFinish={onTest} style={{ marginBottom: 16 }}>
        <Form.Item name="ip" rules={[{ required: true, message: "IP required" }]}>
          <Input placeholder="1.2.3.4" autoComplete="off" style={{ width: 220 }} />
        </Form.Item>
        <Form.Item>
          <Button type="primary" htmlType="submit" loading={trustTest.isPending}>
            Test
          </Button>
        </Form.Item>
      </Form>
      {trustTest.isError && (
        <Alert
          type="error"
          message={t("crowdsectestipcard.test_failed")}
          description={trustTest.error instanceof Error ? trustTest.error.message : "unknown error"}
        />
      )}
      {lastResult && (
        <Table
          size="small"
          rowKey="layer"
          dataSource={lastResult.verdicts}
          pagination={false}
          columns={[
            { title: "Layer", dataIndex: "layer", width: 140 },
            {
              title: "Outcome",
              dataIndex: "outcome",
              width: 110,
              render: (o: string) => (
                <Tag color={o === "deny" ? "red" : o === "allow" ? "green" : "default"}>
                  {o}
                </Tag>
              ),
            },
            { title: "Detail", dataIndex: "detail" },
            {
              title: "",
              dataIndex: "action",
              width: 120,
              render: (_: unknown, row: TrustVerdict) =>
                row.layer === "crowdsec" && row.outcome === "deny" ? (
                  <Popconfirm
                    title={`Unban ${lastResult.ip}?`}
                    description={t("crowdsectestipcard.drops_every_active_crowdsec_decision_targeti")}
                    okText={t("crowdsectestipcard.unban")}
                    okButtonProps={{ danger: true }}
                    onConfirm={() => handleUnban(lastResult.ip)}
                  >
                    <Button size="small" danger loading={unbanIP.isPending}>
                      Unban
                    </Button>
                  </Popconfirm>
                ) : null,
            },
          ]}
          footer={() => (
            <Space>
              <Typography.Text type="secondary">IP tested:</Typography.Text>
              <Typography.Text code>{lastResult.ip}</Typography.Text>
            </Space>
          )}
        />
      )}
    </Card>
  );
};
