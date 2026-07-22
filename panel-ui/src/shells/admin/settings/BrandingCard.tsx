// BrandingCard — M28 Panel Branding tab body.
//
// Two cards:
//   * "Panel Branding" — brand text input + light/dark logo uploader.
//   * Companion "Page Templates" card lives in PageTemplatesCard.tsx.
//
// Logo upload POSTs multipart/form-data to
// /api/v1/admin/settings/branding/logo/:variant. On success we
// invalidate the public /branding key so the topbar re-reads
// immediately. DELETE clears the custom logo; the topbar falls back
// to the built-in SVG.
import { useTranslation } from "react-i18next";
import { useEffect, useState } from "react";
import {
  Alert,
  Button,
  Card,
  Col,
  Form,
  Input,
  Row,
  Space,
  Typography,
  Upload,
  message,
} from "antd";
import type { UploadProps } from "antd";
import { useQueryClient } from "@tanstack/react-query";

import { DeleteOutlined, SaveOutlined, UploadOutlined } from "@icons";

import { apiClient } from "../../../apiClient";
import { useBranding } from "../../../hooks/useBranding";

type ServerSettingsShape = {
  panel_brand_text: string;
};

const LOGO_ACCEPT = "image/png,image/svg+xml,image/webp,image/jpeg,image/gif";
const MAX_LOGO_BYTES = 512 * 1024;

export const BrandingCard = () => {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [form] = Form.useForm<ServerSettingsShape>();
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const branding = useBranding();

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const resp = await apiClient.get<{ panel_brand_text?: string }>(
          "/admin/settings",
        );
        if (cancelled) return;
        form.setFieldsValue({ panel_brand_text: resp.data.panel_brand_text ?? "" });
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

  const handleSubmit = async (values: ServerSettingsShape) => {
    setSaving(true);
    try {
      await apiClient.patch("/admin/settings", {
        panel_brand_text: values.panel_brand_text ?? "",
      });
      qc.invalidateQueries({ queryKey: ["branding", "public"] });
      message.success("Branding text saved");
    } catch (err) {
      message.error(err instanceof Error ? err.message : "Save failed");
    } finally {
      setSaving(false);
    }
  };

  const buildUploader = (variant: "light" | "dark"): UploadProps => ({
    name: "file",
    accept: LOGO_ACCEPT,
    showUploadList: false,
    beforeUpload: (file) => {
      if (file.size > MAX_LOGO_BYTES) {
        message.error(`Logo must be <= ${Math.round(MAX_LOGO_BYTES / 1024)} KB`);
        return Upload.LIST_IGNORE;
      }
      return true;
    },
    customRequest: async ({ file, onSuccess, onError }) => {
      try {
        const fd = new FormData();
        fd.append("file", file as Blob);
        await apiClient.post(`/admin/settings/branding/logo/${variant}`, fd, {
          headers: { "Content-Type": "multipart/form-data" },
        });
        qc.invalidateQueries({ queryKey: ["branding", "public"] });
        message.success(`${variant === "light" ? "Light" : "Dark"} logo uploaded`);
        onSuccess?.({} as unknown);
      } catch (err) {
        const msg = err instanceof Error ? err.message : "Upload failed";
        message.error(msg);
        onError?.(err as Error);
      }
    },
  });

  const clearLogo = async (variant: "light" | "dark") => {
    try {
      await apiClient.delete(`/admin/settings/branding/logo/${variant}`);
      qc.invalidateQueries({ queryKey: ["branding", "public"] });
      message.success(`${variant === "light" ? "Light" : "Dark"} logo cleared`);
    } catch (err) {
      message.error(err instanceof Error ? err.message : "Clear failed");
    }
  };

  const renderLogoPreview = (variant: "light" | "dark") => {
    const hasCustom = variant === "light" ? branding.hasLogoLight : branding.hasLogoDark;
    const bg = variant === "light" ? "#f5f5f5" : "#141414";
    const src = hasCustom
      ? `/api/v1/branding/logo/${variant}?_=${branding.isLoading ? 0 : Date.now()}`
      : variant === "dark"
        ? "/images/jabali_logo_dark.svg"
        : "/images/jabali_logo.svg";
    return (
      <div
        style={{
          background: bg,
          border: "1px solid #d9d9d9",
          borderRadius: 6,
          padding: 16,
          minHeight: 80,
          display: "flex",
          alignItems: "center",
          justifyContent: "center",
        }}
      >
        <img src={src} alt={`${variant} logo preview`} style={{ maxHeight: 48, maxWidth: "100%" }} />
      </div>
    );
  };

  return (
    <>
      <Card title={t("brandingcard.panel_branding")} style={{ marginBottom: 16 }}>
        <Form
          form={form}
          layout="vertical"
          onFinish={handleSubmit}
          disabled={loading}
        >
          <Form.Item
            label={t("brandingcard.brand_text")}
            name="panel_brand_text"
            rules={[{ max: 60, message: "<= 60 chars" }]}
            extra="Shown next to the logo and in the browser tab title. Empty falls back to 'Jabali'."
          >
            <Input placeholder={t("brandingcard.jabali")} maxLength={60} showCount />
          </Form.Item>

          <Space>
            <Button
              type="primary"
              icon={<SaveOutlined />}
              loading={saving}
              htmlType="submit"
            >
              Save
            </Button>
          </Space>
        </Form>

        <Row gutter={[24, 24]} style={{ marginTop: 24 }}>
          <Col xs={24} md={12}>
            <Typography.Title level={5} style={{ marginTop: 0 }}>Light Logo</Typography.Title>
            <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
              Shown when the panel is in light mode. PNG / SVG / WEBP / JPEG, up to 512 KB.
            </Typography.Paragraph>
            {renderLogoPreview("light")}
            <Space style={{ marginTop: 12 }} wrap>
              <Upload {...buildUploader("light")}>
                <Button icon={<UploadOutlined />}>Upload Light Logo</Button>
              </Upload>
              {branding.hasLogoLight && (
                <Button danger icon={<DeleteOutlined />} onClick={() => clearLogo("light")}>
                  Remove
                </Button>
              )}
            </Space>
          </Col>
          <Col xs={24} md={12}>
            <Typography.Title level={5} style={{ marginTop: 0 }}>Dark Logo</Typography.Title>
            <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
              Shown when the panel is in dark mode. PNG / SVG / WEBP / JPEG, up to 512 KB.
            </Typography.Paragraph>
            {renderLogoPreview("dark")}
            <Space style={{ marginTop: 12 }} wrap>
              <Upload {...buildUploader("dark")}>
                <Button icon={<UploadOutlined />}>Upload Dark Logo</Button>
              </Upload>
              {branding.hasLogoDark && (
                <Button danger icon={<DeleteOutlined />} onClick={() => clearLogo("dark")}>
                  Remove
                </Button>
              )}
            </Space>
          </Col>
        </Row>

        <Alert
          type="info"
          showIcon
          style={{ marginTop: 16 }}
          message={t("brandingcard.browser_cache")}
          description={t("brandingcard.newly_uploaded_logos_may_take_up_to_5_minute")}
        />
      </Card>
    </>
  );
};
