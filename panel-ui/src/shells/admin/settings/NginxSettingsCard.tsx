// NginxSettingsCard — Server Settings → Nginx (M55). Server-wide nginx
// tunables. Saving PATCHes /admin/settings with the nginx_* fields; the
// backend dispatches `nginx.tunables.apply` which renders
// /etc/nginx/conf.d/05-jabali-tunables.conf (http scope) + patches the two
// worker-scope knobs into nginx.conf, gated by `nginx -t` with rollback.
//
// The Advanced free-form box drops raw directives into the http{} fragment.
// It is gated only by nginx -t, so a syntactically-valid-but-wrong directive
// can still degrade the server — hence the red warning.
import { useTranslation } from "react-i18next";
import {
  Alert,
  App,
  Button,
  Card,
  Divider,
  Form,
  Input,
  InputNumber,
  Space,
  Spin,
  Switch,
  Typography,
} from "antd";
import { useEffect, useState } from "react";

import { apiClient } from "../../../apiClient";

interface NginxSettings {
  nginx_client_max_body_size: string;
  nginx_keepalive_timeout: string;
  nginx_server_tokens: boolean;
  nginx_gzip: boolean;
  nginx_client_body_timeout: string;
  nginx_client_header_timeout: string;
  nginx_send_timeout: string;
  nginx_proxy_connect_timeout: string;
  nginx_proxy_read_timeout: string;
  nginx_proxy_send_timeout: string;
  nginx_worker_processes: string;
  nginx_worker_connections: number;
  nginx_custom_http: string;
  nginx_cache_max_size_gb: number;
  nginx_cache_keyzone_mb: number;
  nginx_cache_inactive_min: number;
}

// nginx value grammars — mirror the API + agent regex so the UI rejects bad
// input before a round-trip. Size: digits + optional k/m/g. Time: digits +
// optional ms/s/m/h.
const SIZE_RE = /^[0-9]+[kKmMgG]?$/;
const TIME_RE = /^[0-9]+(ms|s|m|h)?$/;
const WORKER_PROC_RE = /^(auto|[1-9][0-9]?)$/;

const sizeRule = { pattern: SIZE_RE, message: "e.g. 50m, 1g, or a number" };
const timeRule = { pattern: TIME_RE, message: "e.g. 60s, 300, 5m" };

export function NginxSettingsCard() {
  const { t } = useTranslation();
  const { message } = App.useApp();
  const [form] = Form.useForm<NginxSettings>();
  const [loaded, setLoaded] = useState(false);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    void (async () => {
      try {
        const r = await apiClient.get<NginxSettings>("/admin/settings");
        form.setFieldsValue(r.data);
        setLoaded(true);
      } catch (err) {
        message.error(
          `Could not load nginx settings: ${err instanceof Error ? err.message : String(err)}`,
        );
      }
    })();
  }, [form, message]);

  const onSave = async () => {
    let values: NginxSettings;
    try {
      values = await form.validateFields();
    } catch {
      return; // antd shows field errors
    }
    setSaving(true);
    try {
      await apiClient.patch("/admin/settings", values);
      message.success("Nginx settings saved. Applying + reloading nginx…");
    } catch (err) {
      message.error(err instanceof Error ? err.message : "Failed to save nginx settings");
    } finally {
      setSaving(false);
    }
  };

  if (!loaded) {
    return (
      <Card title={t("nginxsettingscard.nginx")} style={{ marginBottom: 16 }}>
        <Spin />
      </Card>
    );
  }

  return (
    <Card title={t("nginxsettingscard.nginx")}>
      <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
        Server-wide nginx tunables applied at <code>http&#123;&#125;</code> scope (individual
        sites may override per-directive). Changes are validated with{" "}
        <code>nginx -t</code> and rolled back automatically if the test fails.
      </Typography.Paragraph>

      <Form form={form} layout="vertical" requiredMark={false}>
        <Form.Item
          name="nginx_client_max_body_size"
          label={t("nginxsettingscard.max_upload_size_client_max_body_size")}
          tooltip={t("nginxsettingscard.largest_request_body_nginx_accepts_before_41")}
          rules={[sizeRule]}
        >
          <Input style={{ maxWidth: 200 }} placeholder="50m" />
        </Form.Item>

        <Space size="large" wrap>
          <Form.Item name="nginx_server_tokens" label={t("nginxsettingscard.show_nginx_version_server_tokens")} valuePropName="checked" tooltip={t("nginxsettingscard.off_hides_the_nginx_version_in_responses_err")}>
            <Switch checkedChildren="on" unCheckedChildren="off" />
          </Form.Item>
          <Form.Item name="nginx_gzip" label={t("nginxsettingscard.gzip_compression")} valuePropName="checked">
            <Switch checkedChildren="on" unCheckedChildren="off" />
          </Form.Item>
        </Space>

        <Form.Item name="nginx_keepalive_timeout" label={t("nginxsettingscard.keepalive_timeout")} rules={[timeRule]}>
          <Input style={{ maxWidth: 160 }} placeholder="65s" />
        </Form.Item>

        <Divider plain style={{ marginTop: 8 }}>
          Client timeouts
        </Divider>
        <Space size="large" wrap>
          <Form.Item name="nginx_client_body_timeout" label="client_body_timeout" rules={[timeRule]}>
            <Input style={{ width: 120 }} placeholder="60s" />
          </Form.Item>
          <Form.Item name="nginx_client_header_timeout" label="client_header_timeout" rules={[timeRule]}>
            <Input style={{ width: 120 }} placeholder="60s" />
          </Form.Item>
          <Form.Item name="nginx_send_timeout" label="send_timeout" rules={[timeRule]}>
            <Input style={{ width: 120 }} placeholder="60s" />
          </Form.Item>
        </Space>

        <Divider plain style={{ marginTop: 0 }}>
          Reverse-proxy timeouts (defaults for proxied apps)
        </Divider>
        <Space size="large" wrap>
          <Form.Item name="nginx_proxy_connect_timeout" label="proxy_connect_timeout" rules={[timeRule]}>
            <Input style={{ width: 120 }} placeholder="300s" />
          </Form.Item>
          <Form.Item name="nginx_proxy_read_timeout" label="proxy_read_timeout" rules={[timeRule]}>
            <Input style={{ width: 120 }} placeholder="300s" />
          </Form.Item>
          <Form.Item name="nginx_proxy_send_timeout" label="proxy_send_timeout" rules={[timeRule]}>
            <Input style={{ width: 120 }} placeholder="300s" />
          </Form.Item>
        </Space>

        <Divider plain style={{ marginTop: 0 }}>
          Worker processes (edits <code>nginx.conf</code>)
        </Divider>
        <Space size="large" wrap>
          <Form.Item
            name="nginx_worker_processes"
            label="worker_processes"
            tooltip="'auto' = one per CPU core (recommended), or a fixed number."
            rules={[{ pattern: WORKER_PROC_RE, message: "'auto' or 1-99" }]}
          >
            <Input style={{ width: 120 }} placeholder="auto" />
          </Form.Item>
          <Form.Item name="nginx_worker_connections" label="worker_connections">
            <InputNumber min={64} max={1048576} style={{ width: 140 }} />
          </Form.Item>
        </Space>

        <Divider plain>FastCGI cache capacity (GH #634)</Divider>
        <Space wrap>
          <Form.Item name="nginx_cache_max_size_gb" label="max_size (GB)" tooltip={t("nginxsettingscard.shared_fastcgi_cache_max_on_disk_size")}>
            <InputNumber min={1} max={1024} style={{ width: 140 }} />
          </Form.Item>
          <Form.Item name="nginx_cache_keyzone_mb" label="keys_zone (MB)" tooltip={t("nginxsettingscard.cache_key_metadata_zone_8000_keys_mb")}>
            <InputNumber min={8} max={1024} style={{ width: 140 }} />
          </Form.Item>
          <Form.Item name="nginx_cache_inactive_min" label="inactive (min)" tooltip={t("nginxsettingscard.evict_entries_not_accessed_within_this_windo")}>
            <InputNumber min={1} max={1440} style={{ width: 140 }} />
          </Form.Item>
        </Space>

        <Divider plain>
          Advanced
        </Divider>
        <Alert
          type="error"
          showIcon
          style={{ marginBottom: 12 }}
          message={t("nginxsettingscard.raw_nginx_directives_can_make_the_server_uns")}
          description={
            <>
              Directives below are inserted verbatim into the http&#123;&#125; block. They are
              checked with <code>nginx -t</code> (a failed test rolls back), but a
              syntactically-valid-but-wrong value can still break sites or degrade the server.
              Leave empty unless you know exactly what you are adding.
            </>
          }
        />
        <Form.Item
          name="nginx_custom_http"
          label={t("nginxsettingscard.custom_http_directives")}
          rules={[{ max: 4000, message: "Max 4000 characters" }]}
        >
          <Input.TextArea
            rows={5}
            placeholder={"# one directive per line, e.g.\nlarge_client_header_buffers 4 16k;"}
            style={{ fontFamily: "monospace" }}
          />
        </Form.Item>

        <Button type="primary" loading={saving} onClick={onSave}>
          Save nginx settings
        </Button>
      </Form>
    </Card>
  );
}
