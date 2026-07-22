import { useTranslation } from "react-i18next";
import { useState } from "react";
import {
  Drawer,
  Form,
  Grid,
  Input,
  Radio,
  Button,
  Space,
  Divider,
  Typography,
} from "antd";
import { App } from "antd";
import {
  createCronJob,
  updateCronJob,
  type CronJob,
} from "../../../apiClient";

const SCHEDULE_PRESETS = [
  { label: "Every minute", value: "* * * * *" },
  { label: "Hourly", value: "0 * * * *" },
  { label: "Daily at 3 AM", value: "0 3 * * *" },
  { label: "Weekly (Sun 3 AM)", value: "0 3 * * 0" },
  { label: "Monthly (1st 3 AM)", value: "0 3 1 * *" },
  { label: "Advanced", value: "advanced" },
];

interface CreateCronModalProps {
  open: boolean;
  onClose: () => void;
  onSuccess: () => void;
  initial?: CronJob | null;
}

export const CreateCronModal = ({
  open,
  onClose,
  onSuccess,
  initial,
}: CreateCronModalProps) => {
  const { t } = useTranslation();
  const [form] = Form.useForm();
  const [loading, setLoading] = useState(false);
  const [scheduleMode, setScheduleMode] = useState<string>(
    initial ? (initial.schedule in SCHEDULE_PRESETS.map((p) => p.value) ? initial.schedule : "advanced") : "0 * * * *"
  );
  const [customSchedule, setCustomSchedule] = useState(
    initial && !SCHEDULE_PRESETS.map((p) => p.value).includes(initial.schedule)
      ? initial.schedule
      : ""
  );
  const { message: antMessage } = App.useApp();
  const screens = Grid.useBreakpoint();
  const isDesktop = screens.lg ?? (typeof window !== "undefined" ? window.innerWidth >= 992 : true);

  const isEditing = !!initial;

  const handleSubmit = async (values: {
    name: string;
    command: string;
  }) => {
    setLoading(true);
    try {
      const schedule = scheduleMode === "advanced" ? customSchedule : scheduleMode;

      if (!schedule || schedule.trim() === "") {
        antMessage.error("Please enter a schedule");
        setLoading(false);
        return;
      }

      if (isEditing && initial) {
        await updateCronJob(initial.id, {
          name: values.name,
          command: values.command,
          schedule,
        });
        antMessage.success("Cron job updated successfully");
      } else {
        await createCronJob({
          name: values.name,
          command: values.command,
          schedule,
        });
        antMessage.success("Cron job created successfully");
      }

      form.resetFields();
      setScheduleMode("0 * * * *");
      setCustomSchedule("");
      onSuccess();
    } catch (error: unknown) {
      const err = error as { response?: { data?: { error?: string; field?: string; code?: string; detail?: string } }; message?: string };
      const data = err?.response?.data ?? {};
      const detail: string = data.detail ?? err?.message ?? "Failed to save cron job";
      // Backend response shape: { error: 'validation_failed', field: 'command'|'schedule'|'name', code: <ErrCode>, detail: <string> }
      // Earlier code keyed on detail.includes("command") which silently
      // dropped binary_not_allowed / bad_path_arg into the toast layer
      // (those details start with "first token..." or "path contains...").
      // Now map by `field` first (always set on validation_failed) and
      // fall back to detail-substring + code-substring heuristics.
      const field = data.field;
      const code = data.code ?? data.error;

      // Stable, test-regex-matching headline per code so users see the
      // actionable summary even if the toast scrolls.
      const headlineByCode: Record<string, string> = {
        binary_not_allowed: "Binary not allowed — command must start with wp or php",
        metachar_reject: "Shell metacharacters not allowed in command",
        bad_path_arg: "Invalid path / traversal not allowed — must be an absolute path inside an owned docroot",
        bad_schedule_syntax: "Invalid schedule syntax",
        schedule_too_frequent: "Schedule too frequent — minimum 1-minute step",
        invalid_name: "Invalid name — control characters or empty",
        empty: "Field cannot be empty",
        too_long: "Command too long (max 1024 bytes)",
      };
      const headline = (code && headlineByCode[code]) || detail;

      if (field === "command" || /command|metachar|binary|path|traversal/i.test(detail)) {
        form.setFields([{ name: "command", errors: [headline] }]);
        antMessage.error(headline);
      } else if (field === "schedule" || /schedule/i.test(detail)) {
        form.setFields([{ name: "schedule", errors: [headline] }]);
        antMessage.error(headline);
      } else if (field === "name") {
        form.setFields([{ name: "name", errors: [headline] }]);
        antMessage.error(headline);
      } else {
        antMessage.error(headline);
      }
    } finally {
      setLoading(false);
    }
  };

  const handleModalClose = () => {
    form.resetFields();
    setScheduleMode(initial?.schedule || "0 * * * *");
    setCustomSchedule("");
    onClose();
  };

  return (
    <Drawer
      title={isEditing ? "Edit Cron Job" : "Create Cron Job"}
      open={open}
      onClose={handleModalClose}
      width={isDesktop ? 600 : undefined}
      placement="right"
      destroyOnClose
    >
      <Form
        form={form}
        layout="vertical"
        onFinish={handleSubmit}
        initialValues={{
          name: initial?.name || "",
          command: initial?.command || "",
        }}
      >
        <Form.Item
          label={t("createcronmodal.name")}
          name="name"
          rules={[
            { required: true, message: "Please enter a job name" },
            { max: 100, message: "Name must be 100 characters or less" },
          ]}
        >
          <Input placeholder="e.g., WordPress Cron Cleanup" />
        </Form.Item>

        <Form.Item
          label={t("createcronmodal.command")}
          name="command"
          rules={[
            { required: true, message: "Please enter a command" },
          ]}
        >
          <Input.TextArea
            placeholder="wp cron event run --path=/home/user/example.com/public_html"
            rows={3}
            style={{ fontFamily: "monospace" }}
          />
        </Form.Item>

        <Form.Item>
          <Typography.Text type="secondary">
            Must start with <code>wp</code>, <code>php</code>, or a specific
            version like <code>php8.5</code> (bare <code>php</code> uses your
            assigned version). Shell operators (|, &, $, ...) are rejected.
          </Typography.Text>
        </Form.Item>

        <Divider style={{ margin: "16px 0" }} />

        <Form.Item label={t("createcronmodal.schedule")}>
          <Radio.Group
            value={scheduleMode}
            onChange={(e) => setScheduleMode(e.target.value)}
          >
            {SCHEDULE_PRESETS.map((preset) => (
              <Radio key={preset.value} value={preset.value}>
                {preset.label}
              </Radio>
            ))}
          </Radio.Group>
        </Form.Item>

        {scheduleMode === "advanced" && (
          <Form.Item
            label={t("createcronmodal.cron_expression")}
            rules={[
              { required: true, message: "Please enter a cron expression" },
            ]}
          >
            <Input
              placeholder="0 * * * *"
              value={customSchedule}
              onChange={(e) => setCustomSchedule(e.target.value)}
              style={{ fontFamily: "monospace" }}
            />
          </Form.Item>
        )}

        {scheduleMode === "advanced" && (
          <Form.Item>
            <Typography.Text type="secondary">
              5-field cron expression (minute hour day month weekday).{" "}
              <a href="https://crontab.guru" target="_blank" rel="noopener noreferrer">
                Learn more
              </a>
            </Typography.Text>
          </Form.Item>
        )}

        <Form.Item style={{ marginBottom: 0 }}>
          <Space>
            <Button type="primary" htmlType="submit" loading={loading}>
              {isEditing ? "Update" : "Create"}
            </Button>
            <Button onClick={handleModalClose}>Cancel</Button>
          </Space>
        </Form.Item>
      </Form>
    </Drawer>
  );
};
