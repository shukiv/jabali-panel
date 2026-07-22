// AutoReplyModal — per-mailbox "Automatic Replies" (vacation responder)
// editor. GH #240: autoresponders moved out of their own tab and into the
// Mailboxes list, edited inline per mailbox. Renamed "Automatic Replies"
// to match the Microsoft 365 wording.
import { useTranslation } from "react-i18next";
import { useEffect } from "react";
import {
  Button,
  DatePicker,
  Form,
  Input,
  message,
  Modal,
  Popconfirm,
  Switch,
} from "antd";
import dayjs, { type Dayjs } from "dayjs";

import {
  useUpdateAutoresponder,
  useDeleteAutoresponder,
  type Autoresponder,
} from "../../../hooks/useAutoresponders";

interface FormValues {
  enabled: boolean;
  date_range?: [Dayjs | null, Dayjs | null];
  subject?: string;
  text_body?: string;
  html_body?: string;
}

export interface AutoReplyModalProps {
  open: boolean;
  mailboxId: string;
  email: string;
  /** Current autoresponder for this mailbox, if any (prefills the form). */
  current?: Autoresponder | null;
  onClose: () => void;
}

export const AutoReplyModal = ({
  open,
  mailboxId,
  email,
  current,
  onClose,
}: AutoReplyModalProps) => {
  const { t } = useTranslation();
  const [form] = Form.useForm<FormValues>();
  const updateMut = useUpdateAutoresponder();
  const deleteMut = useDeleteAutoresponder();

  useEffect(() => {
    if (!open) return;
    form.setFieldsValue({
      enabled: current?.enabled ?? true,
      date_range: [
        current?.from_date ? dayjs(current.from_date) : null,
        current?.to_date ? dayjs(current.to_date) : null,
      ],
      subject: current?.subject ?? "",
      text_body: current?.text_body ?? "",
      html_body: current?.html_body ?? "",
    });
  }, [open, current, form]);

  const submit = async () => {
    const vals = await form.validateFields();
    const [from, to] = vals.date_range ?? [null, null];
    try {
      await updateMut.mutateAsync({
        mailboxID: mailboxId,
        input: {
          enabled: vals.enabled,
          from_date: from ? from.toISOString() : null,
          to_date: to ? to.toISOString() : null,
          subject: vals.subject || null,
          text_body: vals.text_body || null,
          html_body: vals.html_body || null,
        },
      });
      message.success("Automatic replies saved");
      onClose();
    } catch (err) {
      const msg =
        (err as { response?: { data?: { error?: string } } })?.response?.data
          ?.error ?? "Failed to save automatic replies";
      message.error(msg);
    }
  };

  const turnOff = async () => {
    try {
      await deleteMut.mutateAsync(mailboxId);
      message.success("Automatic replies turned off");
      onClose();
    } catch (err) {
      const msg =
        (err as { response?: { data?: { error?: string } } })?.response?.data
          ?.error ?? "Failed to turn off";
      message.error(msg);
    }
  };

  const isOn = current?.enabled ?? false;

  return (
    <Modal
      open={open}
      title={`Automatic replies: ${email}`}
      onCancel={onClose}
      onOk={submit}
      okText={t("autoreplymodal.save")}
      confirmLoading={updateMut.isPending}
      destroyOnClose
      width={640}
      footer={[
        isOn && (
          <Popconfirm
            key="off"
            title={`Turn off automatic replies for ${email}?`}
            onConfirm={turnOff}
            okText={t("autoreplymodal.turn_off")}
            okButtonProps={{ danger: true }}
          >
            <Button danger loading={deleteMut.isPending} style={{ float: "left" }}>
              Turn off
            </Button>
          </Popconfirm>
        ),
        <Button key="cancel" onClick={onClose}>
          Cancel
        </Button>,
        <Button
          key="save"
          type="primary"
          loading={updateMut.isPending}
          onClick={submit}
        >
          Save
        </Button>,
      ]}
    >
      <Form form={form} layout="vertical" preserve={false}>
        <Form.Item name="enabled" label={t("autoreplymodal.send_automatic_replies")} valuePropName="checked">
          <Switch />
        </Form.Item>
        <Form.Item name="date_range" label={t("autoreplymodal.active_dates_optional")}>
          <DatePicker.RangePicker showTime style={{ width: "100%" }} />
        </Form.Item>
        <Form.Item name="subject" label={t("autoreplymodal.subject")}>
          <Input placeholder={t("autoreplymodal.out_of_office")} maxLength={200} />
        </Form.Item>
        <Form.Item name="text_body" label={t("autoreplymodal.plain_text_body")}>
          <Input.TextArea rows={4} placeholder={t("autoreplymodal.i_m_away_until")} />
        </Form.Item>
        <Form.Item name="html_body" label={t("autoreplymodal.html_body_optional")}>
          <Input.TextArea rows={4} placeholder="<p>I'm away until...</p>" />
        </Form.Item>
      </Form>
    </Modal>
  );
};
