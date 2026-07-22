// LookAndFeelCard — panel-wide appearance: font size + operator colors. Stored
// as a server_settings singleton and applied for ALL users via the app's
// ConfigProvider (useBranding -> useMuiTheme). Empty color = built-in default.
import { useTranslation } from "react-i18next";
import { useEffect, useState } from "react";

import {
  Button,
  Card,
  Col,
  ColorPicker,
  Form,
  Row,
  Segmented,
  Space,
  Tooltip,
  Typography,
  Divider,
  message,
} from "antd";
import { useQueryClient } from "@tanstack/react-query";

import { QuestionCircleOutlined, SaveOutlined } from "@icons";

import { apiClient } from "../../../apiClient";
import { PANEL_COLORS, PANEL_CHROME_COLORS } from "../../../lib/panelColors";

type FormShape = Record<string, unknown> & {
  panel_font_size: "small" | "medium" | "large";
};

const COLOR_FIELDS = [
  ...PANEL_COLORS.map((c) => c.field),
  ...PANEL_CHROME_COLORS.map((c) => c.field),
];

// ColorPicker yields an antd Color object on change, a hex string on load, and a
// fully-transparent colour when cleared — normalize all to a hex string, mapping
// "cleared / transparent" to "" (= built-in default). Without this, clearing a
// picker would persist a real transparent colour (#00000000) and wash the panel
// out instead of restoring the default.
const toHex = (c: unknown): string => {
  if (!c) return "";
  if (typeof c === "string") return /^#0{6,8}$/i.test(c) ? "" : c;
  if (typeof c === "object" && c !== null) {
    const obj = c as {
      toHexString?: () => string;
      toRgb?: () => { a: number };
    };
    if (typeof obj.toRgb === "function" && obj.toRgb().a === 0) return "";
    if (typeof obj.toHexString === "function") {
      const hex = obj.toHexString();
      return /^#0{6,8}$/i.test(hex) ? "" : hex;
    }
  }
  return "";
};

export const LookAndFeelCard = () => {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [form] = Form.useForm<FormShape>();
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const resp = await apiClient.get<Record<string, string>>(
          "/admin/settings",
        );
        if (cancelled) return;
        const values: Record<string, unknown> = {
          panel_font_size: resp.data.panel_font_size ?? "medium",
        };
        for (const f of COLOR_FIELDS) values[f] = resp.data[f] ?? "";
        form.setFieldsValue(values as Partial<FormShape>);
      } catch (err) {
        message.error(err instanceof Error ? err.message : "Load failed");
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [form]);

  const persist = async (values: FormShape) => {
    setSaving(true);
    try {
      const payload: Record<string, string> = {
        panel_font_size: (values.panel_font_size as string) ?? "medium",
      };
      for (const f of COLOR_FIELDS) payload[f] = toHex(values[f]);
      await apiClient.patch("/admin/settings", payload);
      qc.invalidateQueries({ queryKey: ["branding", "public"] });
      message.success("Look and feel saved");
    } catch (err) {
      message.error(err instanceof Error ? err.message : "Save failed");
    } finally {
      setSaving(false);
    }
  };

  // Reset clears every colour back to the built-in default and saves.
  const resetColors = async () => {
    const cleared: Record<string, undefined> = {};
    for (const f of COLOR_FIELDS) cleared[f] = undefined;
    form.setFieldsValue(cleared as Partial<FormShape>);
    await persist(form.getFieldsValue() as FormShape);
  };

  return (
    <Card title={t("lookandfeelcard.look_and_feel")} style={{ marginBottom: 16 }}>
      <Form form={form} layout="vertical" onFinish={persist} disabled={loading}>
        <Form.Item
          label={t("lookandfeelcard.panel_font_size")}
          name="panel_font_size"
          extra="Applies to the whole panel for all users. Medium is the default."
        >
          <Segmented
            options={[
              { label: "Small", value: "small" },
              { label: "Medium", value: "medium" },
              { label: "Large", value: "large" },
            ]}
          />
        </Form.Item>

        <Row gutter={[24, 0]}>
          {PANEL_COLORS.map((c) => (
            <Col xs={24} md={12} key={c.field}>
              <Form.Item
                name={c.field}
                getValueProps={(v) => ({ value: v || undefined })}
                label={
                  <Space size={4}>
                    {c.label}
                    <Tooltip title={c.tip}>
                      <QuestionCircleOutlined style={{ opacity: 0.55 }} />
                    </Tooltip>
                  </Space>
                }
              >
                <ColorPicker
                  allowClear
                  showText
                  disabledAlpha
                  format="hex"
                  onClear={() => form.setFieldValue(c.field, "")}
                />
              </Form.Item>
            </Col>
          ))}
        </Row>

        <Divider />
        <Typography.Title level={5} style={{ marginTop: 0 }}>
          Panel chrome (per theme)
        </Typography.Title>
        <Typography.Paragraph type="secondary" style={{ fontSize: 12 }}>
          Background, top bar and text colors — separate values for light and
          dark mode, since one color can&apos;t read well in both. Clear = default.
        </Typography.Paragraph>
        <Row gutter={[24, 0]}>
          {PANEL_CHROME_COLORS.map((c) => (
            <Col xs={24} md={12} key={c.field}>
              <Form.Item
                name={c.field}
                getValueProps={(v) => ({ value: v || undefined })}
                label={
                  <Space size={4}>
                    {c.label}
                    <Tooltip title={c.tip}>
                      <QuestionCircleOutlined style={{ opacity: 0.55 }} />
                    </Tooltip>
                  </Space>
                }
              >
                <ColorPicker
                  allowClear
                  showText
                  disabledAlpha
                  format="hex"
                  onClear={() => form.setFieldValue(c.field, "")}
                />
              </Form.Item>
            </Col>
          ))}
        </Row>

        <Space>
          <Button
            type="primary"
            icon={<SaveOutlined />}
            loading={saving}
            htmlType="submit"
          >
            Save
          </Button>
          <Button onClick={resetColors} disabled={saving || loading}>
            Reset colors
          </Button>
        </Space>
      </Form>
    </Card>
  );
};
