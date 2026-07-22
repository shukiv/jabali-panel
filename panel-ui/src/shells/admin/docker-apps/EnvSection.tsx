// EnvSection — environment / credentials editor embedded in the Edit drawer
// (no standalone page). Lists every env var of an installed app, reveals
// secrets, lets you edit a value, and regenerate a generated secret. Apply
// re-renders the compose and recreates the container, so it has its own
// action independent of the ports/limits Save above it.
import { useTranslation } from "react-i18next";
import { App, Button, Input, Popconfirm, Space, Table, Tooltip, Typography } from "antd";
import { CopyOutlined, EyeInvisibleOutlined, EyeOutlined, ReloadOutlined } from "@ant-design/icons";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";

import { getEnv, putEnv, regenerateEnv, type EnvVar } from "./api";

interface Props {
  appId: string;
  /** Re-fetch when the drawer opens so stale secrets aren't shown. */
  active: boolean;
}

export const EnvSection = ({ appId, active }: Props) => {
  const { t } = useTranslation();
  const { message } = App.useApp();
  const qc = useQueryClient();
  const [edits, setEdits] = useState<Record<string, string>>({});
  const [revealed, setRevealed] = useState<Record<string, boolean>>({});

  const envQ = useQuery({
    queryKey: ["docker-app-env", appId],
    queryFn: () => getEnv(appId),
    enabled: active && !!appId,
  });

  useEffect(() => {
    if (active) {
      setEdits({});
      setRevealed({});
    }
  }, [active, appId]);

  const save = useMutation({
    mutationFn: () => putEnv(appId, edits),
    onSuccess: () => {
      message.success("Environment saved — container recreated");
      setEdits({});
      void qc.invalidateQueries({ queryKey: ["docker-app-env", appId] });
      void qc.invalidateQueries({ queryKey: ["admin-docker-apps"] });
    },
    onError: (e: unknown) =>
      message.error(`Save failed: ${(e as { response?: { data?: { detail?: string } } })?.response?.data?.detail ?? e}`),
  });

  const regen = useMutation({
    mutationFn: (key: string) => regenerateEnv(appId, key),
    onSuccess: (res) => {
      message.success(`Regenerated ${res.key}`);
      setRevealed((s) => ({ ...s, [res.key]: true }));
      void qc.invalidateQueries({ queryKey: ["docker-app-env", appId] });
      void qc.invalidateQueries({ queryKey: ["admin-docker-apps"] });
    },
    onError: (e: unknown) =>
      message.error(`Regenerate failed: ${(e as { response?: { data?: { detail?: string } } })?.response?.data?.detail ?? e}`),
  });

  const copy = (v: string) => {
    void navigator.clipboard?.writeText(v);
    message.success("Copied");
  };

  const rows = envQ.data ?? [];
  const dirty = Object.keys(edits).length > 0;

  return (
    <div>
      <Space align="center" style={{ justifyContent: "space-between", width: "100%", marginBottom: 8 }}>
        <Typography.Title level={5} style={{ margin: 0 }}>
          Environment
        </Typography.Title>
        <Popconfirm
          title={t("envsection.recreate_the_container")}
          description={t("envsection.saving_applies_the_new_values_by_recreating")}
          okText={t("envsection.save_recreate")}
          disabled={!dirty}
          onConfirm={() => save.mutate()}
        >
          <Button size="small" type="primary" disabled={!dirty} loading={save.isPending}>
            Save environment ({Object.keys(edits).length})
          </Button>
        </Popconfirm>
      </Space>
      <Typography.Paragraph type="secondary" style={{ marginBottom: 12 }}>
        App credentials live here — reveal a secret to read it, edit a value, or regenerate a
        generated secret. Saving or regenerating recreates the container to apply.
      </Typography.Paragraph>
      <Table<EnvVar>
        size="small"
        rowKey="name"
        loading={envQ.isLoading}
        dataSource={rows}
        pagination={false}
        scroll={{ x: "max-content" }}
        columns={[
          {
            title: "Variable",
            dataIndex: "name",
            render: (n: string, r) => (
              <Space size={4}>
                <Typography.Text code>{n}</Typography.Text>
                {r.generated && <Typography.Text type="secondary">(generated)</Typography.Text>}
              </Space>
            ),
          },
          {
            title: "Value",
            dataIndex: "value",
            render: (v: string, r) => {
              const editing = r.name in edits;
              const shown = revealed[r.name] || !r.secret;
              return (
                <Space.Compact style={{ width: "100%" }}>
                  <Input
                    value={editing ? edits[r.name] : v}
                    type={shown ? "text" : "password"}
                    onChange={(e) => setEdits((s) => ({ ...s, [r.name]: e.target.value }))}
                  />
                  {r.secret && (
                    <Tooltip title={shown ? "Hide" : "Reveal"}>
                      <Button
                        icon={shown ? <EyeInvisibleOutlined /> : <EyeOutlined />}
                        onClick={() => setRevealed((s) => ({ ...s, [r.name]: !shown }))}
                      />
                    </Tooltip>
                  )}
                  <Tooltip title={t("envsection.copy")}>
                    <Button icon={<CopyOutlined />} onClick={() => copy(editing ? edits[r.name] : v)} />
                  </Tooltip>
                </Space.Compact>
              );
            },
          },
          {
            title: "",
            key: "actions",
            width: 120,
            render: (_: unknown, r) =>
              r.generated ? (
                <Popconfirm
                  title={`Regenerate ${r.name}?`}
                  description={t("envsection.mints_a_new_secret_and_recreates_the_contain")}
                  okText={t("envsection.regenerate")}
                  onConfirm={() => regen.mutate(r.name)}
                >
                  <Button size="small" icon={<ReloadOutlined />} loading={regen.isPending && regen.variables === r.name}>
                    Regenerate
                  </Button>
                </Popconfirm>
              ) : null,
          },
        ]}
      />
    </div>
  );
};
