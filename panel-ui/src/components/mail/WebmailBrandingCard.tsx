// WebmailBrandingCard — GH #2502: the webmail branding of one domain, shown
// by Bulwark on mail.<domain>: its name, company name, logos, favicon and
// login-page links. Empty fields keep the server-wide branding.
//
// The owner edits it while their hosting package has "Webmail branding"; an
// admin always can (it then stays even if the package loses the switch).
// The same card sits in the user's Mail → domain → Settings and in the
// admin's domain email section; the API's can_edit decides which view.
import { Alert, Button, Card, Form, Input, Popconfirm, Skeleton, Space, Typography, Upload } from "antd";
import type { UploadProps } from "antd";
import { DeleteOutlined, UploadOutlined } from "@icons";
import { feedback } from "../../lib/feedback"; // GH #970: themed toasts
import {
  useRemoveWebmailBrandingPicture,
  useResetWebmailBranding,
  useSaveWebmailBranding,
  useUploadWebmailBrandingPicture,
  useWebmailBranding,
  webmailBrandingPictureURL,
  type WebmailBranding,
  type WebmailBrandingPicture,
  type WebmailBrandingText,
} from "../../hooks/useWebmailBranding";

// The API's limits (internal/webmailbranding).
const MAX_PICTURE_BYTES = 512 * 1024;
const PICTURE_ACCEPT = "image/png,image/jpeg";

const PICTURES: {
  picture: WebmailBrandingPicture;
  field: "logo_light" | "logo_dark" | "favicon";
  label: string;
  hint: string;
  // The preview box keeps this background in either theme, and so the
  // colour of its placeholder text.
  background: string;
}[] = [
  { picture: "logo-light", field: "logo_light", label: "Logo", hint: "On the login page and in webmail.", background: "#f5f5f5" },
  { picture: "logo-dark", field: "logo_dark", label: "Logo, dark theme", hint: "Empty: the logo above.", background: "#141414" },
  { picture: "favicon", field: "favicon", label: "Favicon", hint: "The browser tab icon; a square picture.", background: "#f5f5f5" },
];

function errorMessage(err: unknown, fallback: string): string {
  const data = (err as { response?: { data?: { error?: string; detail?: string } } })?.response?.data;
  return data?.detail ?? data?.error ?? fallback;
}

function hasBranding(b: WebmailBranding): boolean {
  return [b.app_name, b.company_name, b.website_url, b.privacy_url, b.imprint_url, b.logo_light, b.logo_dark, b.favicon].some(
    (v) => v !== "",
  );
}

const urlRule = { pattern: /^https?:\/\/\S+$/i, message: "Enter a link that starts with https:// or http://" };

// TextForm mounts with the saved values; the parent keys it by them, so a
// save or a reset remounts it with what the server stored.
const TextForm = ({ domainId, saved }: { domainId: string; saved: WebmailBranding }) => {
  const [form] = Form.useForm<WebmailBrandingText>();
  const save = useSaveWebmailBranding(domainId);

  const onFinish = async (vals: WebmailBrandingText) => {
    try {
      await save.mutateAsync({
        app_name: vals.app_name ?? "",
        company_name: vals.company_name ?? "",
        website_url: vals.website_url ?? "",
        privacy_url: vals.privacy_url ?? "",
        imprint_url: vals.imprint_url ?? "",
      });
      feedback.message.success("Webmail branding saved");
    } catch (err) {
      feedback.message.error(errorMessage(err, "Failed to save the webmail branding"));
    }
  };

  return (
    <Form<WebmailBrandingText>
      form={form}
      name={`webmail-branding-${domainId}`}
      layout="vertical"
      style={{ maxWidth: 560 }}
      initialValues={{
        app_name: saved.app_name,
        company_name: saved.company_name,
        website_url: saved.website_url,
        privacy_url: saved.privacy_url,
        imprint_url: saved.imprint_url,
      }}
      onFinish={onFinish}
    >
      <Form.Item name="app_name" label="Name" extra="The webmail's name, in the browser tab and on the login page.">
        <Input maxLength={64} placeholder="Server-wide name" />
      </Form.Item>
      <Form.Item name="company_name" label="Company name" extra="On the login page.">
        <Input maxLength={128} />
      </Form.Item>
      <Form.Item name="website_url" label="Website link" rules={[urlRule]}>
        <Input maxLength={512} placeholder="https://" inputMode="url" />
      </Form.Item>
      <Form.Item name="privacy_url" label="Privacy policy link" rules={[urlRule]}>
        <Input maxLength={512} placeholder="https://" inputMode="url" />
      </Form.Item>
      <Form.Item name="imprint_url" label="Imprint link" rules={[urlRule]}>
        <Input maxLength={512} placeholder="https://" inputMode="url" />
      </Form.Item>
      <Button type="primary" htmlType="submit" loading={save.isPending}>
        Save
      </Button>
    </Form>
  );
};

const PictureRow = ({ domainId, saved, row }: { domainId: string; saved: WebmailBranding; row: (typeof PICTURES)[number] }) => {
  const upload = useUploadWebmailBrandingPicture(domainId);
  const remove = useRemoveWebmailBrandingPicture(domainId);
  const hash = saved[row.field];

  const uploader: UploadProps = {
    name: "file",
    accept: PICTURE_ACCEPT,
    showUploadList: false,
    beforeUpload: (file) => {
      if (file.size > MAX_PICTURE_BYTES) {
        feedback.message.error("The picture must be at most 512 KB");
        return Upload.LIST_IGNORE;
      }
      return true;
    },
    customRequest: async ({ file, onSuccess, onError }) => {
      try {
        await upload.mutateAsync({ picture: row.picture, file: file as Blob });
        feedback.message.success(`${row.label} uploaded`);
        onSuccess?.({} as unknown);
      } catch (err) {
        feedback.message.error(errorMessage(err, "Upload failed"));
        onError?.(err as Error);
      }
    },
  };

  const onRemove = async () => {
    try {
      await remove.mutateAsync(row.picture);
      feedback.message.success(`${row.label} removed`);
    } catch (err) {
      feedback.message.error(errorMessage(err, "Failed to remove the picture"));
    }
  };

  return (
    <Space align="center" wrap size="middle">
      <div
        style={{
          background: row.background,
          border: "1px solid #d9d9d9",
          borderRadius: 6,
          width: 160,
          height: 64,
          display: "flex",
          alignItems: "center",
          justifyContent: "center",
        }}
      >
        {hash ? (
          <img
            src={webmailBrandingPictureURL(domainId, row.picture, hash)}
            alt={`${row.label} preview`}
            style={{ maxHeight: 48, maxWidth: 144 }}
          />
        ) : (
          <Typography.Text style={{ fontSize: 12, color: row.background === "#141414" ? "#bfbfbf" : "#8c8c8c" }}>
            Server-wide
          </Typography.Text>
        )}
      </div>
      <Space direction="vertical" size={4}>
        <Typography.Text strong>{row.label}</Typography.Text>
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          {row.hint}
        </Typography.Text>
        <Space wrap>
          <Upload {...uploader}>
            <Button size="small" icon={<UploadOutlined />} loading={upload.isPending} aria-label={`Upload ${row.label}`}>
              Upload
            </Button>
          </Upload>
          {hash && (
            <Button size="small" danger icon={<DeleteOutlined />} loading={remove.isPending} onClick={onRemove} aria-label={`Remove ${row.label}`}>
              Remove
            </Button>
          )}
        </Space>
      </Space>
    </Space>
  );
};

const Editor = ({ domainId, saved }: { domainId: string; saved: WebmailBranding }) => {
  const reset = useResetWebmailBranding(domainId);
  const onReset = async () => {
    try {
      await reset.mutateAsync();
      feedback.message.success("Webmail branding removed");
    } catch (err) {
      feedback.message.error(errorMessage(err, "Failed to remove the webmail branding"));
    }
  };
  const textKey = [saved.app_name, saved.company_name, saved.website_url, saved.privacy_url, saved.imprint_url].join("\u0000");

  return (
    <Space direction="vertical" size="large" style={{ width: "100%" }}>
      <Typography.Text type="secondary" style={{ fontSize: 12 }}>
        What webmail shows on <Typography.Text code>{saved.host}</Typography.Text>. Empty fields keep the
        server-wide branding. A change reaches webmail within a couple of minutes; webmail restarts for a few
        seconds when it does.
      </Typography.Text>
      {hasBranding(saved) && !saved.applies && (
        <Alert
          type="warning"
          showIcon
          message="Webmail doesn't show this branding"
          description="The owner's hosting package no longer includes webmail branding. Saving it here makes it an administrator's branding, which webmail always shows."
        />
      )}
      <TextForm key={textKey} domainId={domainId} saved={saved} />
      <Space direction="vertical" size="middle">
        {PICTURES.map((row) => (
          <PictureRow key={row.picture} domainId={domainId} saved={saved} row={row} />
        ))}
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          PNG or JPEG, at most 512 KB and 1024 × 1024 pixels.
        </Typography.Text>
      </Space>
      {hasBranding(saved) && (
        <Popconfirm
          title={`Remove the webmail branding of ${saved.host}?`}
          description="Its name, links and pictures are deleted; webmail goes back to the server-wide branding."
          okText="Remove"
          okButtonProps={{ danger: true }}
          onConfirm={onReset}
        >
          <Button danger loading={reset.isPending}>
            Remove branding
          </Button>
        </Popconfirm>
      )}
    </Space>
  );
};

export const WebmailBrandingCard = ({ domainId }: { domainId: string }) => {
  const q = useWebmailBranding(domainId);
  let body;
  if (q.isLoading) {
    body = <Skeleton active paragraph={{ rows: 3 }} />;
  } else if (!q.data) {
    body = <Alert type="error" showIcon message="Failed to load the webmail branding" />;
  } else if (q.data.can_edit) {
    body = <Editor domainId={domainId} saved={q.data} />;
  } else if (q.data.applies) {
    body = (
      <Typography.Text type="secondary">
        Your hosting provider set the webmail branding of <Typography.Text code>{q.data.host}</Typography.Text>.
      </Typography.Text>
    );
  } else {
    body = (
      <Typography.Text type="secondary">
        Your hosting plan doesn't include webmail branding: giving the webmail on{" "}
        <Typography.Text code>{q.data.host}</Typography.Text> its own name, logos and links.
      </Typography.Text>
    );
  }
  return (
    <Card size="small" title="Webmail branding">
      {body}
    </Card>
  );
};
