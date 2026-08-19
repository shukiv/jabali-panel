// DomainCreate — admin form for a new domain.
//
// Intentionally thin: name + user id + optional doc root. The server
// auto-generates doc_root when blank. Post-M21: Form.useForm +
// useCreateMutation, no Refine wrappers.
import { useTranslation } from "react-i18next";
import { Button, Card, Checkbox, Form, Input, Select, Typography } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
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
  // GH #1175: reverse-proxy domain — the panel reserves a loopback port and
  // proxies the domain to it. The assigned port comes back on create.
  reverse_proxy?: boolean;
};

type DomainCreated = { id: string; reverse_proxy_port?: number };

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
      const created = await createMutation.mutateAsync(values);
      feedback.message.success("Domain created");
      // GH #1175: the assigned loopback port is the one thing the operator
      // must act on, so surface it in a modal that stays until dismissed.
      if (values.reverse_proxy && created?.reverse_proxy_port) {
        feedback.modal.success({
          title: "Reverse proxy ready",
          content: `Run the app on 127.0.0.1:${created.reverse_proxy_port}. The panel proxies ${values.name} to that port and keeps the vhost in sync across TLS renewals.`,
        });
      }
      navigate("/jabali-admin/domains");
    } catch (err: unknown) {
      const msg =
        err instanceof Error ? err.message : "Failed to create domain";
      feedback.message.error(msg);
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

        {/* GH #1175: reverse-proxy option. The panel reserves a conflict-free
            loopback port and writes the proxy_pass vhost; the assigned port is
            shown after the domain is created. */}
        <Form.Item
          name="reverse_proxy"
          valuePropName="checked"
          initialValue={false}
          tooltip="Turns this domain into a reverse proxy instead of a website. The panel reserves a local port and forwards the domain to it — run an app (a Docker container, Node service, …) on that port. The assigned port is shown after the domain is created."
        >
          <Checkbox>Set up as a reverse proxy (forward to a local app port)</Checkbox>
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
