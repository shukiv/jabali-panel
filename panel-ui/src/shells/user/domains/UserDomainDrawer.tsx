// UserDomainDrawer — tenant Add-domain Drawer (replaces the
// /jabali-panel/domains/create page route).
import { useTranslation } from "react-i18next";
import {
  Button,
  Checkbox,
  Collapse,
  Drawer,
  Form,
  Grid,
  Input,
  InputNumber,
  Select,
  Space,
} from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
import { useEffect } from "react";

import { apiClient } from "../../../apiClient";
import { useCreateMutation } from "../../../hooks/useQueries";
import { useServerCapabilities } from "../../../hooks/useServerCapabilities";
import { DnsZoneFields } from "../../../components/dns/DnsZoneFields";

type UserDomainCreateInput = {
  name: string;
  // GH #1413: optional custom document root at create time (handy for
  // subdomains). Blank = the default …/domains/<name>/public_html. The
  // server confines a tenant's docroot to this domain's own tree. GH #1541
  // moved it under the drawer's "Advanced" section (out of the simple form).
  doc_root?: string;
  mail_provider?: string;
  m365_onmicrosoft?: string;
  google_dkim?: string;
  // GH #1540: apex IP for a DNS-only zone (dns mode only) — the "pointed IP".
  ip_address?: string;
  // GH #1540 follow-up: optional apex IPv6 (AAAA) for a DNS-only zone.
  ip6_address?: string;
  // GH #1541: create_www is no longer a checkbox — it's derived from the domain
  // (apex ⇒ true, subdomain ⇒ false) and sent as a plain flag.
  create_www?: boolean;
  ssl_mode?: string;
  // GH #1175: reverse-proxy domain. When true the panel reserves a loopback
  // port and proxies the domain to it (no docroot/PHP). The assigned port
  // comes back on the create response. GH #1541: under "Advanced".
  reverse_proxy?: boolean;
  // GH #1401: optional — the specific local port to proxy to (e.g. your app
  // already listens on 6875). Left blank, the panel auto-assigns a free port.
  reverse_proxy_port?: number;
  // GH #1449: Web / Mail / DNS are independent services. Both default ON —
  // the drawer only sends false to opt OUT. web_enabled=false → a docroot-less
  // DNS-only zone or mail-only domain; manage_dns=false → external DNS.
  web_enabled?: boolean;
  manage_dns?: boolean;
  // GH #1541: web-mode "Add Mail Domain" checkbox (default on). A drawer-only
  // field — never sent to POST /domains. Checked ⇒ mail_provider="jabali";
  // unchecked ⇒ the "DNS Template" select's external provider (or none).
  add_mail?: boolean;
  // GH #1479: mail-domain webmail toggle. NOT a create-request field (the model
  // defaults webmail_enabled ON); the drawer only acts on the OFF case, via a
  // follow-up PATCH — setting the tinyint false at create hits GORM's
  // zero-value-omit trap, but the update path persists it explicitly.
  enable_webmail?: boolean;
};
type DomainCreated = { id: string; reverse_proxy_port?: number };

// GH #1449: one drawer, three entry points. "web" is the Add Web Domain flow
// (with Mail/DNS opt-ins); "dns" adds a DNS-only zone; "mail" adds a mail-only
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
  // GH #1409/#1541: when the mail module isn't installed, "Add Mail Domain"
  // isn't a real choice — it's unchecked and disabled, and the user can still
  // add external-mail DNS via the DNS Template select. `!== false` treats the
  // brief pre-load state as installed so a mail-enabled server never flickers.
  const { data: caps } = useServerCapabilities();
  const mailInstalled = caps?.mail_enabled !== false;
  const dnsEnabled = caps?.dns_enabled !== false;
  // GH #1541: "Add Mail Domain" defaults on (matches the checkbox initialValue).
  const addMail = Form.useWatch("add_mail", form) ?? true;
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
    // GH #1541: with mail not installed, "Add Mail Domain" can't be checked —
    // uncheck it (the checkbox is also disabled) so the form opens valid and the
    // DNS Template (external mail) select is available instead.
    if (isWeb && !mailInstalled) form.setFieldValue("add_mail", false);
  }, [open, mailInstalled, isWeb, form]);

  const handleFinish = async (values: UserDomainCreateInput) => {
    try {
      // GH #1479/#1541: enable_webmail and add_mail are drawer-only fields, never
      // create-request fields. Build each mode's payload explicitly so no such
      // field (nor a hidden input antd kept) leaks into POST /domains.
      const wantWebmail = values.enable_webmail;
      let payload: UserDomainCreateInput;
      if (isWeb) {
        const wantMail = values.add_mail ?? true;
        const isReverse = values.reverse_proxy ?? false;
        payload = {
          name: values.name,
          ssl_mode: values.ssl_mode,
          // Add Mail Domain checked → Jabali mail on this server; unchecked → the
          // DNS Template select's external provider (or "none" for no mail).
          mail_provider: wantMail ? "jabali" : (values.mail_provider ?? "none"),
          m365_onmicrosoft: values.m365_onmicrosoft,
          google_dkim: values.google_dkim,
          // Add DNS Zone. Forced off when the DNS module isn't running (the
          // checkbox is hidden then) → external DNS.
          manage_dns: dnsEnabled ? (values.manage_dns ?? true) : false,
          // GH #1541: auto-create the www record for an apex domain (exactly two
          // labels, e.g. example.com), never for a subdomain (blog.example.com).
          // Replaces the old manual "Create www record" checkbox. Heuristic, not
          // a public-suffix lookup: it under-creates www for multi-part-TLD
          // apexes (example.co.uk → 3 labels → skipped) but never creates a wrong
          // www.<subdomain> record. create_www also drives the cert SAN, so it
          // stays independent of manage_dns.
          create_www: values.name.split(".").length === 2,
          // Advanced: a reverse-proxy domain has no document root.
          reverse_proxy: isReverse,
          reverse_proxy_port: isReverse ? values.reverse_proxy_port : undefined,
          doc_root: isReverse ? undefined : values.doc_root,
        };
      } else {
        // GH #1449: a web-off entry (DNS-only zone / mail-only domain) carries
        // only the fields that apply — never web-only inputs antd may have kept.
        payload = {
          name: values.name,
          web_enabled: false,
          manage_dns: isDNS ? true : values.manage_dns,
          // GH #1479/#1540: mail mode is always Jabali mail (the provider select
          // is hidden there, so values.mail_provider isn't registered). A DNS-only
          // zone takes its provider from the DNS Template select (Default → none,
          // or external m365/google).
          mail_provider: isDNS ? (values.mail_provider ?? "none") : "jabali",
          ssl_mode: isDNS ? "none" : values.ssl_mode,
          // GH #1540: the DNS-only zone's apex IP + the template's helper inputs.
          ...(isDNS
            ? {
                ip_address: values.ip_address,
                ip6_address: values.ip6_address,
                m365_onmicrosoft: values.m365_onmicrosoft,
                google_dkim: values.google_dkim,
              }
            : {}),
        };
      }
      const created = await createMutation.mutateAsync(payload);
      // GH #1479: webmail defaults ON at the model, so only the OFF case needs a
      // write — done as a PATCH (not at create) to sidestep GORM's tinyint
      // zero-value-omit trap. Best-effort: the domain is already created.
      if (isMail && wantWebmail === false && created?.id) {
        try {
          await apiClient.patch(`/domains/${created.id}`, { webmail_enabled: false });
        } catch {
          feedback.message.warning(
            "Mail domain added, but disabling webmail failed — turn it off from the domain's settings.",
          );
        }
      }
      feedback.message.success(
        isDNS ? "DNS zone added" : isMail ? "Mail domain added" : "Domain added",
      );
      // GH #1175: the assigned loopback port is the one piece of info the user
      // must act on (bind their app to it), so surface it in a modal that
      // stays until dismissed rather than a toast they might miss.
      if (isWeb && payload.reverse_proxy && created?.reverse_proxy_port) {
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
      title={isDNS ? "Add DNS Zone" : isMail ? "Add Mail Domain" : "Add Web Domain"}
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

        {/* GH #1540: DNS-only zone — the apex IP (prefilled with this server's
            IP) and a DNS Template (Default / Microsoft 365 / Google Workspace).
            Shared with the admin Add DNS Zone drawer. */}
        {isDNS && (
          <DnsZoneFields publicIP={caps?.public_ipv4} publicIPv6={caps?.public_ipv6} />
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
                // GH #1479: 'None' is offered for WEB only. A mail domain needs a
                // real cert on mail.<domain> for IMAPS/SMTPS/autoconfig + webmail,
                // and the server rejects ssl_mode=none while mail is on
                // (ssl_none_with_email), so mail mode shows LE / Self-signed only.
                ...(isWeb ? [{ value: "none", label: "None (HTTP only)" }] : []),
              ]}
            />
          </Form.Item>
        )}

        {/* GH #1541: "Add Mail Domain" — the simple way to add Jabali mail while
            creating the website. Checked by default (disabled + unchecked when the
            mail module isn't installed). Unchecking reveals the DNS Template select
            below for external mail providers. */}
        {isWeb && (
          <Form.Item name="add_mail" valuePropName="checked" initialValue={true}>
            <Checkbox disabled={!mailInstalled}>
              {mailInstalled ? "Add Mail Domain" : "Add Mail Domain (mail module not installed)"}
            </Checkbox>
          </Form.Item>
        )}

        {/* GH #1541: "Add DNS Zone" — host this domain's DNS on this server.
            Checked by default. Hidden when the DNS module is off (external DNS is
            then the only option). Reuses the GH #1449 manage_dns flag. */}
        {isWeb && dnsEnabled && (
          <Form.Item
            name="manage_dns"
            valuePropName="checked"
            initialValue={true}
            tooltip="Host this domain's DNS zone on this server. Uncheck if your DNS is managed elsewhere (e.g. your registrar or Cloudflare)."
          >
            <Checkbox>Add DNS Zone</Checkbox>
          </Form.Item>
        )}

        {/* GH #1541: "DNS Template" — only when Add Mail Domain is unchecked.
            Sets up the mail DNS records for an external provider (Microsoft 365 /
            Google Workspace), or nothing. Reuses the mail_provider field; "jabali"
            is intentionally absent (that's what Add Mail Domain is for). */}
        {isWeb && !addMail && (
          <Form.Item
            label="DNS Template"
            name="mail_provider"
            initialValue="none"
            tooltip="Add DNS records for external mail hosted with Microsoft 365 or Google Workspace. Leave as None if this domain has no mail."
          >
            <Select
              options={[
                { value: "none", label: "None (no external mail)" },
                { value: "m365", label: "Microsoft 365" },
                { value: "google", label: "Google Workspace" },
              ]}
            />
          </Form.Item>
        )}

        {/* Web/mail Template helper inputs. In dns mode these live inside
            DnsZoneFields instead, so gate them out here to avoid a duplicate
            registration of the same field name. */}
        {!isDNS && mailProvider === "m365" && (
          <Form.Item
            label={t("userdomaindrawer.microsoft_365_tenant")}
            name="m365_onmicrosoft"
            tooltip={t("userdomaindrawer.optional_your_tenant_onmicrosoft_com_adds_th")}
          >
            <Input placeholder="contoso.onmicrosoft.com (optional)" />
          </Form.Item>
        )}

        {!isDNS && mailProvider === "google" && (
          <Form.Item
            label={t("userdomaindrawer.google_dkim_value")}
            name="google_dkim"
            tooltip={t("userdomaindrawer.optional_paste_the_google_domainkey_txt_valu")}
          >
            <Input.TextArea rows={2} placeholder="v=DKIM1; k=rsa; p=... (optional)" />
          </Form.Item>
        )}

        {/* GH #1479: webmail toggle for mail domains. Default ON (matches the
            model default); unchecking issues a follow-up PATCH after create. */}
        {isMail && (
          <Form.Item
            name="enable_webmail"
            valuePropName="checked"
            initialValue={true}
            tooltip="Serve the Jabali webmail app at this domain (webmail.<domain>). Uncheck if your users only use their own mail clients."
          >
            <Checkbox>Enable webmail</Checkbox>
          </Form.Item>
        )}

        {/* GH #1449: mail-mode DNS records opt-out (run mail DNS elsewhere). */}
        {isMail && (
          <Form.Item
            name="manage_dns"
            valuePropName="checked"
            initialValue={true}
            tooltip="Create this domain's mail DNS records (MX, SPF, DKIM, autodiscover) on this server's DNS. Uncheck if you manage DNS elsewhere — then point MX/SPF/DKIM at this server yourself."
          >
            <Checkbox>Create DNS mail records</Checkbox>
          </Form.Item>
        )}

        {/* GH #1541: keep the create form to the bare minimum. Document Root and
            the reverse-proxy option are real but rarely needed at create time, so
            they live under a collapsed "Advanced" section. Preview URL is dropped
            entirely — it's a later, per-domain toggle. antd lazily renders the
            panel body, so these fields register only once Advanced is expanded. */}
        {isWeb && (
          <Collapse
            ghost
            style={{ marginBottom: 16 }}
            items={[
              {
                key: "advanced",
                label: "Advanced",
                children: (
                  <>
                    {/* GH #1413: custom document root. Hidden for reverse-proxy
                        domains (they have no docroot). The server confines a
                        tenant's path to this domain's own tree. */}
                    {!reverseProxy && (
                      <Form.Item
                        label={t("userdomaindrawer.document_root")}
                        name="doc_root"
                        tooltip={t("userdomaindrawer.document_root_hint")}
                      >
                        <Input placeholder="Leave blank for the default (…/public_html)" />
                      </Form.Item>
                    )}

                    {/* GH #1175: reverse-proxy option. The panel reserves a
                        conflict-free loopback port and writes the proxy_pass vhost;
                        the assigned port is shown after the domain is added. */}
                    <Form.Item name="reverse_proxy" valuePropName="checked" initialValue={false}>
                      <Checkbox>
                        Set up as a reverse proxy (run your own app on a local port)
                      </Checkbox>
                    </Form.Item>

                    {/* GH #1401: let the tenant type their app's port; blank =
                        auto-assign. Server validates it (range + system-port
                        denylist + not-in-use). */}
                    {reverseProxy && (
                      <Form.Item
                        name="reverse_proxy_port"
                        label="Port (optional)"
                        tooltip="The local port your app listens on (e.g. 6875). Leave blank to let the panel pick a free port for you. Ports below 1024, the panel's own ports, and ports used by system services are not allowed."
                        rules={[
                          {
                            validator: (_, v) =>
                              v == null ||
                              v === "" ||
                              (Number.isInteger(v) && v >= 1024 && v <= 65535)
                                ? Promise.resolve()
                                : Promise.reject(
                                    new Error("Enter a port between 1024 and 65535, or leave blank"),
                                  ),
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
                  </>
                ),
              },
            ]}
          />
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
