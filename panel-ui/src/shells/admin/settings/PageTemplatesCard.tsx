// PageTemplatesCard — M28 admin list of operator-editable page
// templates (default domain index, 404/403/500). List on the left,
// edit in a Modal with a textarea.
import { useTranslation } from "react-i18next";
import { useState } from "react";
import {
  Button,
  Card,
  Input,
  List,
  Modal,
  Popconfirm,
  Space,
  Switch,
  Tag,
  Tooltip,
  Typography,
  message,
} from "antd";
import { useQuery, useQueryClient } from "@tanstack/react-query";

import { EditOutlined, ReloadOutlined } from "@icons";

import { apiClient } from "../../../apiClient";

type PageTemplate = {
  key: string;
  label: string;
  description: string;
  content: string;
  is_default: boolean;
  updated_at?: string;
};

const LIST_KEY = ["admin", "page-templates"] as const;
const SETTINGS_KEY = ["admin", "page-templates", "settings"] as const;
const MAX_BYTES = 128 * 1024;

// GH #860: the unconfigured-domain page only serves when the admin opts in;
// the shipped default drops unknown hosts without a response (444).
const UNCONFIGURED_KEY = "domain_unconfigured";

export const PageTemplatesCard = () => {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [editing, setEditing] = useState<PageTemplate | null>(null);
  const [draft, setDraft] = useState("");
  const [saving, setSaving] = useState(false);

  const list = useQuery<{ data: PageTemplate[] }>({
    queryKey: LIST_KEY,
    queryFn: async () => {
      const { data } = await apiClient.get<{ data: PageTemplate[] }>(
        "/admin/settings/page-templates",
      );
      return data;
    },
  });

  const settings = useQuery<{ unconfigured_page_enabled: boolean }>({
    queryKey: SETTINGS_KEY,
    queryFn: async () => {
      const { data } = await apiClient.get<{
        unconfigured_page_enabled: boolean;
      }>("/admin/settings");
      return data;
    },
  });
  const [togglingUnconfigured, setTogglingUnconfigured] = useState(false);

  const toggleUnconfigured = async (checked: boolean) => {
    setTogglingUnconfigured(true);
    try {
      await apiClient.patch("/admin/settings", {
        unconfigured_page_enabled: checked,
      });
      qc.invalidateQueries({ queryKey: SETTINGS_KEY });
      message.success(
        checked
          ? "Unconfigured-domain page enabled — unknown hosts now get the branded page"
          : "Unconfigured-domain page disabled — unknown hosts are dropped without a response",
      );
    } catch (err) {
      message.error(err instanceof Error ? err.message : "Toggle failed");
    } finally {
      setTogglingUnconfigured(false);
    }
  };

  const openEdit = (row: PageTemplate) => {
    setEditing(row);
    setDraft(row.content);
  };

  const close = () => {
    setEditing(null);
    setDraft("");
  };

  const save = async () => {
    if (!editing) return;
    if (new TextEncoder().encode(draft).length > MAX_BYTES) {
      message.error(`Content exceeds ${Math.round(MAX_BYTES / 1024)} KB`);
      return;
    }
    setSaving(true);
    try {
      await apiClient.patch(`/admin/settings/page-templates/${editing.key}`, {
        content: draft,
      });
      qc.invalidateQueries({ queryKey: LIST_KEY });
      message.success(`${editing.label} saved`);
      close();
    } catch (err) {
      message.error(err instanceof Error ? err.message : "Save failed");
    } finally {
      setSaving(false);
    }
  };

  const reset = async (row: PageTemplate) => {
    try {
      await apiClient.post(`/admin/settings/page-templates/${row.key}/reset`);
      qc.invalidateQueries({ queryKey: LIST_KEY });
      message.success(`${row.label} reset to default`);
    } catch (err) {
      message.error(err instanceof Error ? err.message : "Reset failed");
    }
  };

  return (
    <>
      <Card title={t("pagetemplatescard.page_templates")} style={{ marginBottom: 16 }}>
        <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
          Edit the HTML written to disk on new domains and rendered for common
          error pages. The domain default index supports{" "}
          <code>{"{{.Domain}}"}</code>, <code>{"{{.Username}}"}</code>, and{" "}
          <code>{"{{.DocRoot}}"}</code> placeholders.
        </Typography.Paragraph>
        <List<PageTemplate>
          loading={list.isLoading}
          dataSource={list.data?.data ?? []}
          rowKey="key"
          renderItem={(row) => (
            <List.Item
              actions={[
                ...(row.key === UNCONFIGURED_KEY
                  ? [
                      <Tooltip
                        key="serve"
                        title="Off (default): unknown hosts are dropped with no response, so scanners can't fingerprint the server. On: they get this page instead."
                      >
                        <Space size={6}>
                          <Typography.Text type="secondary">
                            Serve
                          </Typography.Text>
                          <Switch
                            size="small"
                            checked={
                              settings.data?.unconfigured_page_enabled ?? false
                            }
                            loading={
                              togglingUnconfigured || settings.isLoading
                            }
                            onChange={toggleUnconfigured}
                          />
                        </Space>
                      </Tooltip>,
                    ]
                  : []),
                <Button
                  key="edit"
                  icon={<EditOutlined />}
                  onClick={() => openEdit(row)}
                >
                  Edit
                </Button>,
                <Popconfirm
                  key="reset"
                  title={`Reset ${row.label} to built-in default?`}
                  onConfirm={() => reset(row)}
                >
                  <Button icon={<ReloadOutlined />} disabled={row.is_default}>
                    Reset
                  </Button>
                </Popconfirm>,
              ]}
            >
              <List.Item.Meta
                title={
                  <Space>
                    <Typography.Text strong>{row.label}</Typography.Text>
                    {row.is_default ? (
                      <Tag color="default">default</Tag>
                    ) : (
                      <Tag color="blue">customised</Tag>
                    )}
                  </Space>
                }
                description={row.description}
              />
            </List.Item>
          )}
        />
      </Card>

      <Modal
        open={!!editing}
        title={editing ? `Edit — ${editing.label}` : ""}
        onCancel={close}
        width={900}
        confirmLoading={saving}
        onOk={save}
        okText={t("pagetemplatescard.save")}
        cancelText={t("pagetemplatescard.cancel")}
        destroyOnClose
      >
        {editing && (
          <>
            <Typography.Paragraph type="secondary">
              {editing.description}
            </Typography.Paragraph>
            <Input.TextArea
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
              spellCheck={false}
              autoSize={{ minRows: 14 }}
              styles={{
                textarea: {
                  fontFamily:
                    "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace",
                  fontSize: 13,
                  lineHeight: 1.5,
                },
              }}
            />
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              {new TextEncoder().encode(draft).length} / {MAX_BYTES} bytes
            </Typography.Text>
          </>
        )}
      </Modal>
    </>
  );
};
