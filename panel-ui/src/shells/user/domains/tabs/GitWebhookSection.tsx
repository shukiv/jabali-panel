// GitWebhookSection — deploy on push, in a domain's Git tab (GH #2277,
// ADR-0178). GET /domains/:id/git/webhook shows the webhook's URL and what
// its last delivery did; POST turns it on, or gives it a new secret, and
// DELETE turns it off.
//
// The panel shows a secret it made only once, in the POST's answer, so this
// keeps it in a dialog until the user closes it. A secret the Git host made
// (GitLab's signing token, a Bitbucket generated secret) is sent instead and
// never comes back.
import { useState } from "react";
import type { ReactNode } from "react";
import { Alert, Button, Descriptions, Form, Input, Modal, Popconfirm, Space, Tag, Typography } from "antd";
import { KeyOutlined, LinkOutlined, PoweroffOutlined, SyncOutlined } from "@icons";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { apiClient } from "../../../../apiClient";
import { CollapsibleAlert } from "../../../../components/CollapsibleAlert";
import { Timestamp } from "../../../../components/Timestamp";
import { feedback } from "../../../../lib/feedback";

export type GitWebhook = {
  configured: boolean;
  url?: string;
  // Only in the answer to a POST that made the secret.
  secret?: string;
  queued: boolean;
  last_delivery_at: string | null;
  // started, queued, ignored, duplicate, failed, or refused:<error code>.
  last_result: string;
  created_at?: string;
};

// Why a delivery was refused (refused:<code>).
const REFUSED: Record<string, string> = {
  git_deploy_not_in_package: "the hosting package no longer includes Git deployment",
  git_deploy_requires_package: "the account has no hosting package",
  account_suspended: "the account is suspended",
  no_account: "the account has no Linux user",
  git_not_configured: "no repository is saved",
  repo_host_not_allowed: "the server won't connect to the repository's host",
  invalid_repo_url: "the saved repository URL isn't valid",
  token_unreadable: "the saved token can't be read: save it again",
  sso_key_unavailable: "the server can't read the saved token",
};

const lastResultLabel = (result: string, branch: string): { color: string; text: string } => {
  if (result.startsWith("refused:")) {
    const code = result.slice("refused:".length);
    return { color: "red", text: `Refused: ${REFUSED[code] ?? code.replace(/_/g, " ")}` };
  }
  switch (result) {
    case "started":
      return { color: "green", text: "Started a deployment" };
    case "queued":
      return { color: "blue", text: "Waited for the running deployment" };
    case "ignored":
      return { color: "default", text: `Ignored: not a push to ${branch}` };
    case "duplicate":
      return { color: "default", text: "Ignored: received before" };
    case "failed":
      return { color: "red", text: "The deployment couldn't start" };
    default:
      return { color: "default", text: result };
  }
};

type Shown = { url: string; secret: string; replaced: boolean };

export function GitWebhookSection({ domainId, branch }: { domainId: string; branch: string }) {
  const qc = useQueryClient();
  const url = `/domains/${domainId}/git/webhook`;
  const hookQ = useQuery<GitWebhook>({
    queryKey: ["git-webhook", domainId],
    queryFn: async () => (await apiClient.get<GitWebhook>(url)).data,
  });
  const hook = hookQ.data;

  // The secret the panel just made, until the dialog is closed.
  const [shown, setShown] = useState<Shown | null>(null);
  const [ownOpen, setOwnOpen] = useState(false);

  const create = useMutation({
    mutationFn: async (secret?: string) =>
      (await apiClient.post<GitWebhook>(url, secret ? { secret } : {})).data,
    onSuccess: (saved, secret) => {
      const replaced = !!hook?.configured;
      // The cache never holds the secret: only the dialog does.
      qc.setQueryData(["git-webhook", domainId], { ...saved, secret: undefined });
      setOwnOpen(false);
      if (saved.secret && saved.url) {
        setShown({ url: saved.url, secret: saved.secret, replaced });
      } else if (secret) {
        feedback.message.success("Webhook saved with your Git host's secret");
      }
    },
    onError: (err: Error) => feedback.message.error(err.message),
  });
  const making = create.isPending && !create.variables;

  const remove = useMutation({
    mutationFn: async () => {
      await apiClient.delete(url);
    },
    onSuccess: () => {
      feedback.message.success("Deploy on push turned off");
      void qc.invalidateQueries({ queryKey: ["git-webhook", domainId] });
    },
    onError: (err: Error) => feedback.message.error(err.message),
  });

  let body: ReactNode = null;
  if (hookQ.isError || (!hookQ.isLoading && !hook)) {
    body = <Alert type="error" showIcon message="Couldn't load the webhook." />;
  } else if (hook && !hook.configured) {
    body = (
      <Space direction="vertical" size="middle" style={{ width: "100%" }}>
        <Typography.Paragraph type="secondary" style={{ margin: 0 }}>
          Deploy the {branch} branch every time it is pushed. Turn this on, then add a webhook on your Git host
          with the URL and secret the panel gives you.
        </Typography.Paragraph>
        <Space wrap>
          <Button icon={<LinkOutlined />} loading={making} onClick={() => create.mutate(undefined)}>
            Turn on
          </Button>
          <Button icon={<KeyOutlined />} onClick={() => setOwnOpen(true)}>
            Use my Git host&apos;s secret
          </Button>
        </Space>
      </Space>
    );
  } else if (hook) {
    const result = hook.last_result ? lastResultLabel(hook.last_result, branch) : null;
    body = (
      <Space direction="vertical" size="middle" style={{ width: "100%" }}>
        <Descriptions
          bordered
          size="small"
          column={1}
          items={[
            {
              key: "url",
              label: "Webhook URL",
              children: (
                <Typography.Text code copyable style={{ wordBreak: "break-all" }}>
                  {hook.url}
                </Typography.Text>
              ),
            },
            {
              key: "last",
              label: "Last delivery",
              children: hook.last_delivery_at ? (
                <Space size={[8, 4]} wrap>
                  <Timestamp value={hook.last_delivery_at} />
                  {result && <Tag color={result.color}>{result.text}</Tag>}
                </Space>
              ) : (
                <Typography.Text type="secondary">None yet</Typography.Text>
              ),
            },
            ...(hook.queued
              ? [
                  {
                    key: "queued",
                    label: "Waiting",
                    children: "A push waits for the running deployment, and deploys when it ends.",
                  },
                ]
              : []),
          ]}
        />
        <Space wrap>
          <Popconfirm
            title="Make a new secret?"
            description="The old secret stops working at once: put the new one on your Git host."
            okText="New secret"
            onConfirm={() => create.mutate(undefined)}
          >
            <Button icon={<SyncOutlined />} loading={making}>
              New secret
            </Button>
          </Popconfirm>
          <Button icon={<KeyOutlined />} onClick={() => setOwnOpen(true)}>
            Use my Git host&apos;s secret
          </Button>
          <Popconfirm
            title="Turn off deploy on push?"
            description="Pushes stop deploying. Remove the webhook on your Git host too, or its deliveries will fail."
            okText="Turn off"
            okButtonProps={{ danger: true }}
            onConfirm={() => remove.mutate()}
          >
            <Button danger icon={<PoweroffOutlined />} loading={remove.isPending}>
              Turn off
            </Button>
          </Popconfirm>
        </Space>
        <CollapsibleAlert
          storageKey="domain.git.webhook.hosts"
          type="info"
          showIcon
          message="Adding the webhook on your Git host"
          description={
            <ul style={{ margin: 0, paddingInlineStart: 20 }}>
              <li>
                GitHub: Settings → Webhooks. Content type <strong>application/json</strong> (the default,
                form-encoded, is refused), the secret in Secret.
              </li>
              <li>
                GitLab: Settings → Webhooks, push events. Either the panel&apos;s secret in Secret token, or
                GitLab&apos;s signing token given to the panel with Use my Git host&apos;s secret.
              </li>
              <li>Bitbucket: Repository settings → Webhooks, trigger Repository push, with the secret.</li>
              <li>Gitea, Forgejo: Settings → Webhooks, POST, content type application/json, the secret in Secret.</li>
              <li>
                The Git host must reach the panel at this URL and accept its certificate. A delivery the panel
                has received before (Redeliver) deploys nothing: use Deploy now instead.
              </li>
            </ul>
          }
        />
      </Space>
    );
  }

  return (
    <div>
      <Typography.Title level={5}>Deploy on push</Typography.Title>
      {body}

      {shown && (
        <Modal
          open
          title={shown.replaced ? "New webhook secret" : "Deploy on push is on"}
          onCancel={() => setShown(null)}
          maskClosable={false}
          footer={
            <Button type="primary" onClick={() => setShown(null)}>
              Done
            </Button>
          }
        >
          <Space direction="vertical" size="middle" style={{ width: "100%" }}>
            <Alert type="warning" showIcon message="Copy the secret now. It is not shown again." />
            <div>
              <Typography.Text strong>Webhook URL</Typography.Text>
              <Typography.Paragraph code copyable style={{ wordBreak: "break-all", marginBottom: 0 }}>
                {shown.url}
              </Typography.Paragraph>
            </div>
            <div>
              <Typography.Text strong>Secret</Typography.Text>
              <Typography.Paragraph code copyable style={{ wordBreak: "break-all", marginBottom: 0 }}>
                {shown.secret}
              </Typography.Paragraph>
            </div>
          </Space>
        </Modal>
      )}

      {ownOpen && (
        <OwnSecretDialog
          saving={create.isPending && !!create.variables}
          onCancel={() => setOwnOpen(false)}
          onSave={(secret) => create.mutate(secret)}
        />
      )}
    </div>
  );
}

function OwnSecretDialog({
  saving,
  onCancel,
  onSave,
}: {
  saving: boolean;
  onCancel: () => void;
  onSave: (secret: string) => void;
}) {
  const [form] = Form.useForm<{ secret: string }>();
  return (
    <Modal
      open
      title="Use my Git host's secret"
      okText="Save"
      okButtonProps={{ loading: saving }}
      onOk={() => form.submit()}
      onCancel={onCancel}
      destroyOnHidden
    >
      <Form form={form} layout="vertical" onFinish={(v) => onSave(v.secret.trim())}>
        <Form.Item
          name="secret"
          label="Secret"
          extra="GitLab's signing token (whsec_…) or a secret your Git host generated. It replaces the webhook's secret and is never shown again."
          rules={[
            { required: true, message: "Paste the secret" },
            { min: 16, message: "At least 16 characters" },
            { max: 255, message: "At most 255 characters" },
            { pattern: /^[\x21-\x7e]+$/, message: "Printable characters only, no spaces" },
          ]}
        >
          <Input.Password autoComplete="new-password" spellCheck={false} />
        </Form.Item>
      </Form>
    </Modal>
  );
}
