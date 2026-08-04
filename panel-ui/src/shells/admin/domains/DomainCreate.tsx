// DomainCreate — admin form for a new domain.
//
// Intentionally thin: name + user id + optional doc root. The server
// auto-generates doc_root when blank. Post-M21: Form.useForm +
// useCreateMutation, no Refine wrappers.
import { useTranslation } from "react-i18next";
import { Button, Card, Checkbox, Form, Input, Select, Typography, message } from "antd";
import { useQuery } from "@tanstack/react-query";
import { apiClient } from "../../../apiClient";
import { useNavigate } from "react-router";

import { useCreateMutation } from "../../../hooks/useQueries";

export type DomainCreateInput = {
  name: string;
  user_id: string;
  doc_root?: string;
  mail_provider?: string;
  m365_onmicrosoft?: string;
  google_dkim?: string;
  create_www?: boolean;
  temp_url_enabled?: boolean;
  ssl_mode?: string;
};

type DomainCreated = { id: string };

export const DomainCreate = () => {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const [form] = Form.useForm<DomainCreateInput>();
  const mailProvider = Form.useWatch("mail_provider", form) ?? "jabali";
  const createMutation = useCreateMutation<DomainCreated, DomainCreateInput>({
    resource: "domains",
  });
  const usersQ = useQuery({
    queryKey: ["admin-users-for-domain-create"],
    queryFn: async () => {
      const { data } = await apiClient.get<{ data?: { id: string; email: string; username?: string }[] }>(
        "/users?page_size=500&is_admin=false",
      );
      return data.data ?? [];
    },
  });

  const handleFinish = async (values: DomainCreateInput) => {
    try {
      await createMutation.mutateAsync(values);
      message.success("Domain created");
      navigate("/jabali-admin/domains");
    } catch (err: unknown) {
      const msg =
        err instanceof Error ? err.message : "Failed to create domain";
      message.error(msg);
    }
  };

  return (
    <Card>
      <Typography.Title level={3} style={{ marginTop: 0 }}>
        Create domain
      </Typography.Title>
      <Form<DomainCreateInput>
        form={form}
        layout="vertical"
        onFinish={handleFinish}
      >
        <Form.Item
          label={t("domaincreate.name")}
          name="name"
          // GH #884: stored lowercase; normalize live so the admin sees the
          // canonical value and an autocorrected capital can't slip through.
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

        <Form.Item
          label={t("domaincreate.user")}
          name="user_id"
          rules={[{ required: true, message: "User is required" }]}
        >
          <Select
            showSearch
            placeholder={t("domaincreate.select_a_user")}
            optionFilterProp="label"
            loading={usersQ.isLoading}
            options={(usersQ.data ?? []).map((u) => ({
              value: u.id,
              label: u.username ? `${u.email} (${u.username})` : u.email,
            }))}
          />
        </Form.Item>

        <Form.Item
          label={t("domaincreate.doc_root")}
          name="doc_root"
          tooltip={t("domaincreate.leave_empty_for_auto_generated_path")}
        >
          <Input placeholder="auto-generated if empty" />
        </Form.Item>

        <Form.Item
          label={t("domaincreate.mail")}
          name="mail_provider"
          initialValue="jabali"
          tooltip={t("domaincreate.where_this_domain_s_email_is_hosted_none_and")}
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
            label={t("domaincreate.microsoft_365_tenant")}
            name="m365_onmicrosoft"
            tooltip={t("domaincreate.optional_your_tenant_onmicrosoft_com_adds_th")}
          >
            <Input placeholder="contoso.onmicrosoft.com (optional)" />
          </Form.Item>
        )}

        {mailProvider === "google" && (
          <Form.Item
            label={t("domaincreate.google_dkim_value")}
            name="google_dkim"
            tooltip={t("domaincreate.optional_paste_the_google_domainkey_txt_valu")}
          >
            <Input.TextArea rows={2} placeholder="v=DKIM1; k=rsa; p=... (optional)" />
          </Form.Item>
        )}

        <Form.Item
          label={t("domaincreate.tls_certificate")}
          name="ssl_mode"
          initialValue="le"
          tooltip={t("domaincreate.let_s_encrypt_recommended_self_signed_or_non")}
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
          tooltip={t("domaincreate.adds_a_www_cname_pointing_at_the_domain_apex")}
        >
          <Checkbox>Create www record</Checkbox>
        </Form.Item>

        <Form.Item
          name="temp_url_enabled"
          valuePropName="checked"
          initialValue={false}
          tooltip="Serves the site at <domain-slug>.preview.<panel-hostname> so it can be checked before DNS points here."
        >
          <Checkbox>Enable preview URL</Checkbox>
        </Form.Item>

        <Form.Item>
          <Button
            type="primary"
            htmlType="submit"
            loading={createMutation.isPending}
          >
            Save
          </Button>
        </Form.Item>
      </Form>
    </Card>
  );
};
