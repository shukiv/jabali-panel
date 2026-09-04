// UserDomainDrawer — tenant Add-domain Drawer (replaces the
// /jabali-panel/domains/create page route).
import { useTranslation } from "react-i18next";
import { Button, Checkbox, Drawer, Form, Grid, Input, InputNumber, Select, Space } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
import { useEffect } from "react";

import { useCreateMutation } from "../../../hooks/useQueries";
import { useServerCapabilities } from "../../../hooks/useServerCapabilities";

type UserDomainCreateInput = {
  name: string;
  // GH #1413: optional custom document root at create time (handy for
  // subdomains). Blank = the default …/domains/<name>/public_html. The
  // server confines a tenant's docroot to this domain's own tree.
  doc_root?: string;
  mail_provider?: string;
  m365_onmicrosoft?: string;
  google_dkim?: string;
  temp_url_enabled?: boolean;
  create_www?: boolean;
  ssl_mode?: string;
  // GH #1175: reverse-proxy domain. When true the panel reserves a loopback
  // port and proxies the domain to it (no docroot/PHP). The assigned port
  // comes back on the create response.
  reverse_proxy?: boolean;
  // GH #1401: optional — the specific local port to proxy to (e.g. your app
  // already listens on 6875). Left blank, the panel auto-assigns a free port.
  reverse_proxy_port?: number;
  // GH #1449: Web / Mail / DNS are independent services. Both default ON —
  // the drawer only sends false to opt OUT. web_enabled=false → a docroot-less
  // DNS-only zone or mail-only domain; manage_dns=false → external DNS.
  web_enabled?: boolean;
  manage_dns?: boolean;
};
type DomainCreated = { id: string; reverse_proxy_port?: number };

// GH #1449: one drawer, three entry points. "web" is the classic Add Web
// Domain (with opt-outs); "dns" adds a DNS-only zone; "mail" adds a mail-only
// domain. The mode presets web_enabled / mail_provider and hides the fields
// that don't apply.
export type DomainDrawerMode = "web" | "dns" | "mail";

export interface UserDomainDrawerProps {
  open: boolean;
  onClose: () => void;
  mode?: DomainDrawerMode;
}

export const UserDomainDrawer = ({ open, onClose, mode = "web" }: UserDomainDrawerProps) => {
  const { t } = useTranslation();
  const [form] = Form.useForm<UserDomainCreateInput>();
  const isWeb = mode === "web";
  const isDNS = mode === "dns";
  const isMail = mode === "mail";
  // GH #1409: when the mail module isn't installed, Jabali Mail isn't a real
  // choice — default to None and disable it. `!== false` treats the brief
  // pre-load state as installed so a mail-enabled server never flickers to None.
  const { data: caps } = useServerCapabilities();
  const mailInstalled = caps?.mail_enabled !== false;
  const mailProvider =
    Form.useWatch("mail_provider", form) ?? (mailInstalled ? "jabali" : "none");
  const reverseProxy = Form.useWatch("reverse_proxy", form) ?? false;
  const screens = Grid.useBreakpoint();
  const isDesktop = screens.lg ?? (typeof window !== "undefined" ? window.innerWidth >= 992 : true);

  const createMutation = useCreateMutation<DomainCreated, UserDomainCreateInput>({
    resource: "domains",
  });

  useEffect(() => {
    if (!open) return;
    form.resetFields();
    // Override the static Jabali-Mail default when mail isn't installed.
    if (!mailInstalled) form.setFieldValue("mail_provider", "none");
  }, [open, mailInstalled, form]);

  const handleFinish = async (values: UserDomainCreateInput) => {
    try {
      // A reverse-proxy domain has no document root; drop any value the field
      // may have kept (antd preserves unmounted field values by default) so we
      // never send a docroot the server would validate but never use.
      let payload: UserDomainCreateInput = values.reverse_proxy
        ? { ...values, doc_root: undefined }
        : values;
      // GH #1449: a web-off entry (DNS-only zone / mail-only domain) must not
      // carry web-only fields antd may have kept for hidden inputs.
      if (!isWeb) {
        payload = {
          name: values.name,
          web_enabled: false,
          manage_dns: isDNS ? true : values.manage_dns,
          mail_provider: isDNS ? "none" : values.mail_provider,
          m365_onmicrosoft: values.m365_onmicrosoft,
          google_dkim: values.google_dkim,
          ssl_mode: isDNS ? "none" : values.ssl_mode,
        };
      }
      const created = await createMutation.mutateAsync(payload);
      feedback.message.success(
        isDNS ? "DNS zone added" : isMail ? "Mail domain added" : "Domain added",
      );
      // GH #1175: the assigned loopback port is the one piece of info the user
      // must act on (bind their app to it), so surface it in a modal that
      // stays until dismissed rather than a toast they might miss.
      if (values.reverse_proxy && created?.reverse_proxy_port) {
        feedback.modal.success({
          title: "Reverse proxy ready",
          content: `Run your app on 127.0.0.1:${created.reverse_proxy_port}. The panel proxies ${values.name} to that port and keeps the vhost in sync across TLS renewals.`,
        });
      }
      onClose();
    } catch (err) {
      feedback.message.error(err instanceof Error ? err.message : "Failed to add domain");
    }
  };

  return (
    <Drawer
      title={isDNS ? "Add DNS Zone" : isMail ? "Add Mail Domain" : t("userdomaindrawer.add_domain")}
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
          // GH #884: domains are stored lowercase (the server normalizes too);
          // lowercasing as the user types shows the canonical value and avoids
          // a mobile-autocorrect capital slipping through.
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

        {/* GH #1413: custom document root. Hidden for reverse-proxy domains
            (they have no docroot). The server confines a tenant's path to this
            domain's own tree, so pointing at another domain or elsewhere in the
            home is rejected. */}
        {isWeb && !reverseProxy && (
          <Form.Item
            label={t("userdomaindrawer.document_root")}
            name="doc_root"
            tooltip={t("userdomaindrawer.document_root_hint")}
          >
            <Input placeholder="Leave blank for the default (…/public_html)" />
          </Form.Item>
        )}

        {(isWeb || isMail) && (
        <Form.Item
          label={t("userdomaindrawer.mail")}
          name="mail_provider"
          initialValue="jabali"
          tooltip={t("userdomaindrawer.where_this_domain_s_email_is_hosted_none_and")}
        >
          <Select
            options={[
              {
                value: "jabali",
                label: mailInstalled
                  ? "Jabali mail (this server)"
                  : "Jabali mail (not installed)",
                disabled: !mailInstalled,
              },
              { value: "none", label: "No mail" },
              { value: "m365", label: "Microsoft 365" },
              { value: "google", label: "Google Workspace" },
            ]}
          />
        </Form.Item>
        )}

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

        {(isWeb || isMail) && (
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
              ...(isWeb ? [{ value: "none", label: "None (HTTP only)" }] : []),
            ]}
          />
        </Form.Item>
        )}

        {/* GH #1449: opt out of Jabali DNS (run DNS elsewhere). Not shown for
            the DNS-only flow — there, hosting DNS is the whole point. */}
        {(isWeb || isMail) && (
          <Form.Item
            name="manage_dns"
            valuePropName="checked"
            initialValue={true}
            tooltip="Host this domain's DNS zone on this server. Uncheck if your DNS is managed elsewhere (e.g. your registrar or Cloudflare)."
          >
            <Checkbox>Manage DNS here</Checkbox>
          </Form.Item>
        )}

        {isWeb && (
        <Form.Item
          name="create_www"
          valuePropName="checked"
          initialValue={false}
          tooltip={t("userdomaindrawer.adds_a_www_cname_pointing_at_the_domain_apex")}
        >
          <Checkbox>Create www record</Checkbox>
        </Form.Item>
        )}

        {isWeb && (
        <Form.Item
          name="temp_url_enabled"
          valuePropName="checked"
          initialValue={false}
          tooltip="Serves the site at a preview address under this server's hostname, so you can check it before your domain's DNS points here."
        >
          <Checkbox>Enable preview URL</Checkbox>
        </Form.Item>
        )}

        {/* GH #1175: reverse-proxy option. The panel reserves a conflict-free
            loopback port and writes the proxy_pass vhost; the assigned port is
            shown after the domain is added. */}
        {isWeb && (
        <Form.Item
          name="reverse_proxy"
          valuePropName="checked"
          initialValue={false}
          tooltip="Turns this domain into a reverse proxy instead of a website. The panel reserves a local port and forwards the domain to it — run your own app (a Docker container, Node service, …) on that port. The assigned port is shown after you add the domain."
        >
          <Checkbox>Set up as a reverse proxy (run your own app on a local port)</Checkbox>
        </Form.Item>
        )}

        {/* GH #1401: let the tenant type their app's port; blank = auto-assign.
            Server validates it (range + system-port denylist + not-in-use). */}
        {reverseProxy && (
          <Form.Item
            name="reverse_proxy_port"
            label="Port (optional)"
            tooltip="The local port your app listens on (e.g. 6875). Leave blank to let the panel pick a free port for you. Ports below 1024, the panel's own ports, and ports used by system services are not allowed."
            rules={[
              {
                validator: (_, v) =>
                  v == null || v === "" || (Number.isInteger(v) && v >= 1024 && v <= 65535)
                    ? Promise.resolve()
                    : Promise.reject(new Error("Enter a port between 1024 and 65535, or leave blank")),
              },
            ]}
          >
            <InputNumber
              min={1024}
              max={65535}
              style={{ width: "100%" }}
              placeholder="Auto-assign a free port"
            />
          </Form.Item>
        )}

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
