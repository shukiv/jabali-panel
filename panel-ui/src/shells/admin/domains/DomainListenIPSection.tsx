// DomainListenIPSection — admin per-domain IPv4/IPv6 listen-IP picker
// (M24 Step 9). Two AntD Selects with a "use server default" sentinel
// option per family. Submitting "default" PATCHes listen_ipv*_id: null,
// which the API resolves to the family default at render time.
import { Button, Form, Select, Space, Tag, Typography } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
import { useEffect } from "react";

import { useListQuery, useUpdateMutation } from "../../../hooks/useQueries";

type IPSummary = { id: number; address: string };

type ManagedIP = {
  id: number;
  address: string;
  family: "ipv4" | "ipv6";
  label: string;
  is_default: boolean;
};

type DomainPatchInput = {
  listen_ipv4_id?: number | null;
  listen_ipv6_id?: number | null;
};

interface DomainListenIPSectionProps {
  /** Domain ID — used as the URL param of the PATCH call. */
  domainId: string;
  /** Current binding from the GET response — controls the picker initial state. */
  listenIPv4ID?: number | null;
  listenIPv6ID?: number | null;
  /** Denormalized {id,address} blob for "what address are we serving today" hint. */
  listenIPv4?: IPSummary | null;
  listenIPv6?: IPSummary | null;
  /** Resource path: "admin/ips" for the admin shell, "user/ips" for the
   * (future) user shell. Lets the same component drive both. */
  ipsResource?: string;
  /** Domain resource path — same split for the two shells. */
  domainResource?: string;
}

// "default" is the sentinel value submitted via Select onChange when
// the admin picks "Use server default". It's a string so it can't
// collide with any numeric IP id; the form mapper turns it into a
// JSON null on the wire.
const DEFAULT_SENTINEL = "default";

export const DomainListenIPSection = ({
  domainId,
  listenIPv4ID,
  listenIPv6ID,
  listenIPv4,
  listenIPv6,
  ipsResource = "admin/ips",
  domainResource = "domains",
}: DomainListenIPSectionProps) => {
  const [form] = Form.useForm<{ ipv4: number | "default"; ipv6: number | "default" }>();

  const v4Query = useListQuery<ManagedIP>({
    resource: ipsResource,
    params: { family: "ipv4", page: 1, page_size: 100 },
  });
  const v6Query = useListQuery<ManagedIP>({
    resource: ipsResource,
    params: { family: "ipv6", page: 1, page_size: 100 },
  });

  const updateMutation = useUpdateMutation<unknown, DomainPatchInput>({
    resource: domainResource,
  });

  // Seed form values when the domain (or the picker re-mounts after a
  // save). Pre-M24 domains have null listen_ipv*_id → "default" sentinel.
  useEffect(() => {
    form.setFieldsValue({
      ipv4: listenIPv4ID ?? DEFAULT_SENTINEL,
      ipv6: listenIPv6ID ?? DEFAULT_SENTINEL,
    });
  }, [form, listenIPv4ID, listenIPv6ID]);

  const handleFinish = async (values: {
    ipv4: number | "default";
    ipv6: number | "default";
  }) => {
    const patch: DomainPatchInput = {
      listen_ipv4_id: values.ipv4 === DEFAULT_SENTINEL ? null : values.ipv4,
      listen_ipv6_id: values.ipv6 === DEFAULT_SENTINEL ? null : values.ipv6,
    };
    try {
      await updateMutation.mutateAsync({ id: domainId, input: patch });
      feedback.message.success("Listen-IP binding updated");
    } catch (err: unknown) {
      feedback.message.error(err instanceof Error ? err.message : "Failed to update binding");
    }
  };

  // Build the dropdown options: one "Use server default" entry at the
  // top (with the family default's address inline so the admin sees
  // what null actually maps to), then every IP from the pool.
  const defaultV4 = v4Query.items.find((ip) => ip.is_default);
  const defaultV6 = v6Query.items.find((ip) => ip.is_default);

  return (
    <div>
      <Typography.Paragraph type="secondary">
        Bind this domain to a specific IPv4 / IPv6 from the managed pool. Leave
        either picker on <em>Use server default</em> to fall back to the family
        default — which lets you swap the server primary without re-touching
        every domain.
      </Typography.Paragraph>

      <Space direction="vertical" size={4} style={{ marginBottom: 16 }}>
        {listenIPv4 ? (
          <Tag color="blue">
            Effective IPv4: <code>{listenIPv4.address}</code>
          </Tag>
        ) : null}
        {listenIPv6 ? (
          <Tag color="purple">
            Effective IPv6: <code>{listenIPv6.address}</code>
          </Tag>
        ) : null}
      </Space>

      <Form
        form={form}
        layout="vertical"
        onFinish={handleFinish}
        initialValues={{
          ipv4: listenIPv4ID ?? DEFAULT_SENTINEL,
          ipv6: listenIPv6ID ?? DEFAULT_SENTINEL,
        }}
      >
        <Form.Item label="Listen IPv4" name="ipv4">
          <Select
            loading={v4Query.isLoading}
            placeholder="Use server default"
            options={[
              {
                value: DEFAULT_SENTINEL,
                label: defaultV4
                  ? `Use server default (${defaultV4.address})`
                  : "Use server default",
              },
              ...v4Query.items.map((ip) => ({
                value: ip.id,
                label: ip.label ? `${ip.address} — ${ip.label}` : ip.address,
              })),
            ]}
          />
        </Form.Item>

        <Form.Item label="Listen IPv6" name="ipv6">
          <Select
            loading={v6Query.isLoading}
            placeholder="Use server default"
            options={[
              {
                value: DEFAULT_SENTINEL,
                label: defaultV6
                  ? `Use server default (${defaultV6.address})`
                  : "Use server default",
              },
              ...v6Query.items.map((ip) => ({
                value: ip.id,
                label: ip.label ? `${ip.address} — ${ip.label}` : ip.address,
              })),
            ]}
          />
        </Form.Item>

        <Form.Item>
          <Button
            type="primary"
            htmlType="submit"
            loading={updateMutation.isPending}
          >
            Save listen IPs
          </Button>
        </Form.Item>
      </Form>
    </div>
  );
};
