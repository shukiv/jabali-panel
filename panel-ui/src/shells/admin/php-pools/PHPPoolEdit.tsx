// PHPPoolEdit — admin edit for a per-(user, php-version) FPM pool.
//
// Main form tweaks pm_mode / pm_max_children / idle timeout. A nested
// INI overrides table lives below the save button, driven by its own
// useListQuery + direct apiClient POST/DELETE calls.
import { useTranslation } from "react-i18next";
import { useEffect, useState } from "react";
import {
  Button,
  Card,
  Form,
  Input,
  InputNumber,
  Modal,
  Select,
  Space,
  Spin,
  Table,
  Tag,
  Typography,
  message,
  theme,
} from "antd";
import { useNavigate, useParams } from "react-router";

import { DeleteOutlined } from "@icons";
import { apiClient } from "../../../apiClient";
import { RowActionButton } from "../../../components/RowActionButton";
import {
  useListQuery,
  useOneQuery,
  useUpdateMutation,
} from "../../../hooks/useQueries";

type PHPPoolInput = {
  pm_mode: string;
  pm_max_children: number;
  process_idle_timeout_seconds: number;
  pm_start_servers: number;
  pm_min_spare_servers: number;
  pm_max_spare_servers: number;
  pm_max_requests: number;
  request_terminate_timeout_seconds: number;
  php_version?: string;
};

type PHPPoolRecord = PHPPoolInput & { id: string };

type IniOverride = {
  id: string;
  directive: string;
  value: string;
  kind: "value" | "flag";
};

type IniOverrideInput = {
  directive: string;
  value: string;
  kind: "value" | "flag";
};

export const PHPPoolEdit = () => {
  const { t } = useTranslation();
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const [form] = Form.useForm<PHPPoolInput>();
  const pmMode = Form.useWatch("pm_mode", form);
  const { token } = theme.useToken();

  const { data: pool, isLoading } = useOneQuery<PHPPoolRecord>({
    resource: "php-pools",
    id,
  });
  const updateMutation = useUpdateMutation<PHPPoolRecord, PHPPoolInput>({
    resource: "php-pools",
  });

  // INI overrides are mounted as their own sub-resource; useListQuery
  // handles the list envelope unwrap. Refetch after add/delete.
  const overridesQ = useListQuery<IniOverride>({
    resource: id ? `php-pools/${id}/ini-overrides` : "",
    enabled: !!id,
  });

  const [isOverrideModalOpen, setIsOverrideModalOpen] = useState(false);
  const [overrideForm, setOverrideForm] = useState<IniOverrideInput>({
    directive: "",
    value: "",
    kind: "value",
  });

  useEffect(() => {
    if (pool) {
      form.setFieldsValue(pool);
    }
  }, [pool, form]);

  const handleFinish = async (values: PHPPoolInput) => {
    if (!id) return;
    try {
      await updateMutation.mutateAsync({ id, input: values });
      message.success("Pool updated");
      navigate("/jabali-admin/php-pools");
    } catch (err: unknown) {
      const msg =
        err instanceof Error ? err.message : "Failed to update pool";
      message.error(msg);
    }
  };

  const handleAddOverride = async () => {
    if (!overrideForm.directive.trim() || !id) {
      return;
    }
    try {
      await apiClient.post(
        `/php-pools/${id}/ini-overrides`,
        overrideForm,
      );
      setOverrideForm({ directive: "", value: "", kind: "value" });
      setIsOverrideModalOpen(false);
      await overridesQ.refetch();
    } catch (err) {
      const msg =
        err instanceof Error ? err.message : "Failed to add override";
      message.error(msg);
    }
  };

  const handleDeleteOverride = async (overrideId: string) => {
    if (!id) return;
    try {
      await apiClient.delete(
        `/php-pools/${id}/ini-overrides/${overrideId}`,
      );
      await overridesQ.refetch();
    } catch (err) {
      const msg =
        err instanceof Error ? err.message : "Failed to delete override";
      message.error(msg);
    }
  };

  if (isLoading && !pool) {
    return (
      <div
        style={{
          display: "flex",
          alignItems: "center",
          justifyContent: "center",
          minHeight: 240,
        }}
      >
        <Spin />
      </div>
    );
  }

  return (
    <Card>
      <Typography.Title level={3} style={{ marginTop: 0 }}>
        Edit PHP pool
      </Typography.Title>
      <Form<PHPPoolInput>
        form={form}
        layout="vertical"
        onFinish={handleFinish}
      >
        <Form.Item
          label={t("phppooledit.process_mode")}
          name="pm_mode"
          rules={[{ required: true, message: "Process mode is required" }]}
        >
          <Select placeholder={t("phppooledit.select_process_manager_mode")}>
            <Select.Option value="static">static</Select.Option>
            <Select.Option value="dynamic">dynamic</Select.Option>
            <Select.Option value="ondemand">ondemand</Select.Option>
          </Select>
        </Form.Item>

        <Form.Item
          label={t("phppooledit.max_children")}
          name="pm_max_children"
          rules={[{ required: true, message: "Max children is required" }]}
        >
          <InputNumber min={1} placeholder={t("phppooledit.number_of_child_processes")} />
        </Form.Item>

        <Form.Item
          label={t("phppooledit.process_idle_timeout_seconds")}
          name="process_idle_timeout_seconds"
          rules={[{ required: false }]}
        >
          <InputNumber min={0} placeholder={t("phppooledit.idle_timeout_in_seconds")} />
        </Form.Item>

        {pmMode === "dynamic" && (
          <>
            <Form.Item label={t("phppooledit.start_servers_dynamic")} name="pm_start_servers">
              <InputNumber min={1} placeholder="pm.start_servers" />
            </Form.Item>
            <Form.Item label={t("phppooledit.min_spare_servers_dynamic")} name="pm_min_spare_servers">
              <InputNumber min={1} placeholder="pm.min_spare_servers" />
            </Form.Item>
            <Form.Item label={t("phppooledit.max_spare_servers_dynamic_max_children")} name="pm_max_spare_servers">
              <InputNumber min={1} placeholder="pm.max_spare_servers" />
            </Form.Item>
          </>
        )}

        <Form.Item label={t("phppooledit.max_requests_per_worker_0_never")} name="pm_max_requests">
          <InputNumber min={0} placeholder="pm.max_requests" />
        </Form.Item>

        <Form.Item label={t("phppooledit.request_terminate_timeout_seconds_0_no_limit")} name="request_terminate_timeout_seconds">
          <InputNumber min={0} placeholder="request_terminate_timeout" />
        </Form.Item>

        <Form.Item label={t("phppooledit.php_version")} name="php_version">
          <Input disabled />
        </Form.Item>

        <Form.Item>
          <Button
            type="primary"
            htmlType="submit"
            loading={updateMutation.isPending}
          >
            Save
          </Button>
        </Form.Item>
      </Form>

      {/* INI Overrides Section */}
      <div style={{ marginTop: 32, paddingTop: 24, borderTop: `1px solid ${token.colorBorder}` }}>
        <Space
          wrap
          align="center"
          style={{
            marginBottom: 16,
            width: "100%",
            justifyContent: "space-between",
          }}
        >
          <h3 style={{ margin: 0 }}>INI Overrides</h3>
          <Button type="primary" onClick={() => setIsOverrideModalOpen(true)}>
            Add Override
          </Button>
        </Space>

        <Table<IniOverride>
          columns={[
            { dataIndex: "directive", title: "Directive", key: "directive" },
            {
              dataIndex: "value",
              title: "Value",
              key: "value",
              render: (value: string) => (
                <div
                  style={{
                    maxWidth: 300,
                    overflow: "hidden",
                    textOverflow: "ellipsis",
                    whiteSpace: "nowrap",
                  }}
                >
                  {value}
                </div>
              ),
            },
            {
              dataIndex: "kind",
              title: "Kind",
              key: "kind",
              render: (kind: string) => (
                <Tag color={kind === "flag" ? "orange" : "blue"}>{kind}</Tag>
              ),
            },
            {
              title: "Actions",
              key: "actions",
              width: 100,
              render: (_, record) => (
                <RowActionButton
                  danger
                  icon={<DeleteOutlined />}
                  onClick={() => handleDeleteOverride(record.id)}
                >
                  Delete
                </RowActionButton>
              ),
            },
          ]}
          dataSource={overridesQ.items}
          loading={overridesQ.isLoading}
          rowKey="id"
          pagination={false}
          scroll={{ x: "max-content" }}
        />

        <Modal
          title={t("phppooledit.add_ini_override")}
          open={isOverrideModalOpen}
          onOk={handleAddOverride}
          onCancel={() => {
            setIsOverrideModalOpen(false);
            setOverrideForm({ directive: "", value: "", kind: "value" });
          }}
        >
          <Form layout="vertical">
            <Form.Item label={t("phppooledit.directive")}>
              <Input
                placeholder="e.g., memory_limit"
                value={overrideForm.directive}
                onChange={(e) =>
                  setOverrideForm({
                    ...overrideForm,
                    directive: e.target.value,
                  })
                }
              />
            </Form.Item>
            <Form.Item label={t("phppooledit.value")}>
              <Input
                placeholder="e.g., 256M"
                value={overrideForm.value}
                onChange={(e) =>
                  setOverrideForm({ ...overrideForm, value: e.target.value })
                }
              />
            </Form.Item>
            <Form.Item label={t("phppooledit.kind")}>
              <Select
                value={overrideForm.kind}
                onChange={(value) =>
                  setOverrideForm({ ...overrideForm, kind: value })
                }
              >
                <Select.Option value="value">value</Select.Option>
                <Select.Option value="flag">flag</Select.Option>
              </Select>
            </Form.Item>
          </Form>
        </Modal>
      </div>
    </Card>
  );
};
