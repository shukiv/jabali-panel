// AdminDNSZoneDrawer — admin "Add DNS Zone" Drawer (GH #1540). johnnyq asked
// for an Add DNS Zone button on both the tenant and admin DNS-zone lists, with
// the same simple form: Domain Name / IP Address (prefilled with the panel's
// IP) / Template. The admin form adds one field the tenant form can't have — an
// Owner (user) picker — since an admin creates zones on behalf of a tenant.
//
// It creates a web-off, DNS-managed domain via POST /domains (resource
// "domains"), reusing the exact same backend path as the tenant dns-mode drawer
// and the shared DnsZoneFields body.
import { useQuery } from "@tanstack/react-query";
import { Button, Drawer, Form, Grid, Input, Select, Space } from "antd";

import { apiClient } from "../../../apiClient";
import { feedback } from "../../../lib/feedback";
import { useCreateMutation } from "../../../hooks/useQueries";
import { useServerCapabilities } from "../../../hooks/useServerCapabilities";
import { DnsZoneFields } from "../../../components/dns/DnsZoneFields";

type AdminDNSZoneInput = {
  name: string;
  user_id: string;
  ip_address?: string;
  ip6_address?: string;
  mail_provider?: string;
  m365_onmicrosoft?: string;
  google_dkim?: string;
};

type DomainCreated = { id: string };

export interface AdminDNSZoneDrawerProps {
  open: boolean;
  onClose: () => void;
}

export const AdminDNSZoneDrawer = ({ open, onClose }: AdminDNSZoneDrawerProps) => {
  const [form] = Form.useForm<AdminDNSZoneInput>();
  const { data: caps } = useServerCapabilities();
  const screens = Grid.useBreakpoint();
  const isDesktop = screens.lg ?? (typeof window !== "undefined" ? window.innerWidth >= 992 : true);

  const createMutation = useCreateMutation<DomainCreated, Record<string, unknown>>({
    resource: "domains",
  });

  const usersQ = useQuery({
    queryKey: ["admin-users-for-dns-zone-create"],
    queryFn: async () => {
      const { data } = await apiClient.get<{
        data?: { id: string; email: string; username?: string }[];
      }>("/users?page_size=500&is_admin=false");
      return data.data ?? [];
    },
    enabled: open,
  });

  const handleFinish = async (values: AdminDNSZoneInput) => {
    try {
      // A DNS-only zone: web off, DNS on, no TLS. The apex IP and the mail
      // provider (from the Template select) go straight to POST /domains, which
      // validates the IP (bare IPv4, web-off) and seeds the apex A in the
      // reconciler. mail_provider defaults to "none" (Default template).
      await createMutation.mutateAsync({
        name: values.name,
        user_id: values.user_id,
        web_enabled: false,
        manage_dns: true,
        ssl_mode: "none",
        ip_address: values.ip_address,
        ip6_address: values.ip6_address,
        mail_provider: values.mail_provider ?? "none",
        m365_onmicrosoft: values.m365_onmicrosoft,
        google_dkim: values.google_dkim,
      });
      feedback.message.success("DNS zone added");
      onClose();
    } catch (err) {
      feedback.message.error(err instanceof Error ? err.message : "Failed to add DNS zone");
    }
  };

  return (
    <Drawer
      title="Add DNS Zone"
      open={open}
      onClose={onClose}
      width={isDesktop ? 480 : undefined}
      placement="right"
      destroyOnClose
    >
      <Form<AdminDNSZoneInput> form={form} layout="vertical" onFinish={handleFinish}>
        <Form.Item
          label="Owner"
          name="user_id"
          rules={[{ required: true, message: "Owner is required" }]}
        >
          <Select
            showSearch
            placeholder="Select a user"
            optionFilterProp="label"
            loading={usersQ.isLoading}
            options={(usersQ.data ?? []).map((u) => ({
              value: u.id,
              label: u.username ? `${u.email} (${u.username})` : u.email,
            }))}
          />
        </Form.Item>

        <Form.Item
          label="Domain Name"
          name="name"
          // GH #884: stored lowercase; normalize live so an autocorrected capital
          // can't slip through.
          normalize={(v) => (typeof v === "string" ? v.toLowerCase() : v)}
          rules={[
            { required: true, message: "Domain name is required" },
            { max: 253, message: "Domain name cannot exceed 253 characters" },
            {
              pattern: /^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$/,
              message: "Enter a valid domain name (e.g. example.com)",
            },
          ]}
        >
          <Input placeholder="e.g., example.com" />
        </Form.Item>

        <DnsZoneFields publicIP={caps?.public_ipv4} publicIPv6={caps?.public_ipv6} />

        <Form.Item>
          <Space>
            <Button type="primary" htmlType="submit" loading={createMutation.isPending}>
              Add
            </Button>
            <Button onClick={onClose}>Cancel</Button>
          </Space>
        </Form.Item>
      </Form>
    </Drawer>
  );
};
