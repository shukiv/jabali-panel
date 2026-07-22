// UserDomainDrawer — tenant Add-domain Drawer (replaces the
// /jabali-panel/domains/create page route).
import { useTranslation } from "react-i18next";
import { Button, Checkbox, Drawer, Form, Grid, Input, Select, Space, message } from "antd";
import { useEffect } from "react";

import { useCreateMutation } from "../../../hooks/useQueries";

type UserDomainCreateInput = {
  name: string;
  mail_provider?: string;
  m365_onmicrosoft?: string;
  google_dkim?: string;
  create_www?: boolean;
  ssl_mode?: string;
};
type DomainCreated = { id: string };

export interface UserDomainDrawerProps {
  open: boolean;
  onClose: () => void;
}

export const UserDomainDrawer = ({ open, onClose }: UserDomainDrawerProps) => {
  const { t } = useTranslation();
  const [form] = Form.useForm<UserDomainCreateInput>();
  const mailProvider = Form.useWatch("mail_provider", form) ?? "jabali";
  const screens = Grid.useBreakpoint();
  const isDesktop = screens.lg ?? (typeof window !== "undefined" ? window.innerWidth >= 992 : true);

  const createMutation = useCreateMutation<DomainCreated, UserDomainCreateInput>({
    resource: "domains",
  });

  useEffect(() => {
    if (open) form.resetFields();
  }, [open, form]);

  const handleFinish = async (values: UserDomainCreateInput) => {
    try {
      await createMutation.mutateAsync(values);
      message.success("Domain added");
      onClose();
    } catch (err) {
      message.error(err instanceof Error ? err.message : "Failed to add domain");
    }
  };

  return (
    <Drawer
      title={t("userdomaindrawer.add_domain")}
      open={open}
      onClose={onClose}
      width={isDesktop ? 480 : undefined}
      placement="right"
      destroyOnClose
    >
      <Form<UserDomainCreateInput> form={form} layout="vertical" onFinish={handleFinish}>
        <Form.Item
          label={t("userdomaindrawer.domain_name")}
          name="name"
          rules={[
            { required: true, message: "Domain name is required" },
            { max: 253, message: "Domain name cannot exceed 253 characters" },
            {
              pattern: /^(?:[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,63}$/,
              message: "Enter a valid domain name (e.g. example.com)",
            },
          ]}
        >
          <Input placeholder="e.g., example.com" />
        </Form.Item>

        <Form.Item
          label={t("userdomaindrawer.mail")}
          name="mail_provider"
          initialValue="jabali"
          tooltip={t("userdomaindrawer.where_this_domain_s_email_is_hosted_none_and")}
        >
          <Select
            options={[
              { value: "jabali", label: "Jabali mail (this server)" },
              { value: "none", label: "No mail" },
              { value: "m365", label: "Microsoft 365" },
              { value: "google", label: "Google Workspace" },
            ]}
          />
        </Form.Item>

        {mailProvider === "m365" && (
          <Form.Item
            label={t("userdomaindrawer.microsoft_365_tenant")}
            name="m365_onmicrosoft"
            tooltip={t("userdomaindrawer.optional_your_tenant_onmicrosoft_com_adds_th")}
          >
            <Input placeholder="contoso.onmicrosoft.com (optional)" />
          </Form.Item>
        )}

        {mailProvider === "google" && (
          <Form.Item
            label={t("userdomaindrawer.google_dkim_value")}
            name="google_dkim"
            tooltip={t("userdomaindrawer.optional_paste_the_google_domainkey_txt_valu")}
          >
            <Input.TextArea rows={2} placeholder="v=DKIM1; k=rsa; p=... (optional)" />
          </Form.Item>
        )}

        <Form.Item
          label={t("userdomaindrawer.tls_certificate")}
          name="ssl_mode"
          initialValue="le"
          tooltip={t("userdomaindrawer.let_s_encrypt_issues_a_free_trusted_certific")}
        >
          <Select
            options={[
              { value: "le", label: "Let's Encrypt (recommended)" },
              { value: "self", label: "Self-signed" },
              { value: "none", label: "None (HTTP only)" },
            ]}
          />
        </Form.Item>

        <Form.Item
          name="create_www"
          valuePropName="checked"
          initialValue={false}
          tooltip={t("userdomaindrawer.adds_a_www_cname_pointing_at_the_domain_apex")}
        >
          <Checkbox>Create www record</Checkbox>
        </Form.Item>

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
