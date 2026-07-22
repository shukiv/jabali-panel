// InstallDrawer — picks a catalog entry and submits an install
// request. Renders one editable row per catalog-declared port so the
// admin can flip Enabled / Bind / Host port / Reverse-proxy before
// committing. See ADR-0116 Decision 5.
import { useTranslation } from "react-i18next";
import { Alert, App, Button, Drawer, Form, Input, InputNumber, Select, Space, Switch, Table, Tag, Typography } from "antd";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useState } from "react";

import { apiClient } from "../../../apiClient";

import { useServerStatus } from "../../../hooks/useServerStatus";
import { installApp } from "./api";
import type { CatalogEntry, InstallPortOverride, InstallRequest } from "./types";

interface Props {
  open: boolean;
  entry: CatalogEntry | null;
  onClose: () => void;
  onInstalled?: () => void;
}

interface PortRow extends InstallPortOverride {
  // catalog metadata for read-only columns
  container_port: number;
  protocol: "tcp" | "udp";
}

export const InstallDrawer = ({ open, entry, onClose, onInstalled }: Props) => {
  const { t } = useTranslation();
  const { message } = App.useApp();
  const qc = useQueryClient();
  const destsQ = useQuery({
    queryKey: ["admin-backup-destinations-for-docker-app"],
    queryFn: async () => {
      const { data } = await apiClient.get<{ data?: { id: string; name: string; kind: string }[] }>(
        "/admin/backup-destinations?pageSize=100",
      );
      return data.data ?? [];
    },
  });
  const domainsQ = useQuery({
    queryKey: ["admin-domains-for-docker-app"],
    queryFn: async () => {
      const { data } = await apiClient.get<{ data?: { id: string; name: string }[] }>(
        "/domains?pageSize=500",
      );
      return data.data ?? [];
    },
  });
  const [form] = Form.useForm();
  const watchedDomain = Form.useWatch<string | undefined>("domain", form);
  // The agent silently caps the compose `cpus` limit to the host core
  // count (Docker rejects a higher value). Surface the effective value
  // so an admin who types e.g. "2" on a 1-core box understands why the
  // app still installs at 1 — instead of an invisible clamp (GH#178).
  const statusQ = useServerStatus({ enabled: open });
  const hostCPUs = statusQ.data?.host?.cpu_count;
  const watchedCPU = Form.useWatch<string | undefined>("cpu_limit", form);
  const cpuCapNote = useMemo(() => {
    if (!hostCPUs || !watchedCPU) return undefined;
    const v = parseFloat(watchedCPU);
    if (!Number.isFinite(v) || v <= hostCPUs) return undefined;
    return `Capped to ${hostCPUs} on this host — only ${hostCPUs} core${hostCPUs === 1 ? "" : "s"} available.`;
  }, [hostCPUs, watchedCPU]);
  const [ports, setPorts] = useState<PortRow[]>([]);
  const [envVals, setEnvVals] = useState<Record<string, string>>({});

  useEffect(() => {
    if (!entry) {
      setPorts([]);
      setEnvVals({});
      return;
    }
    // Seed port rows from the catalog defaults whenever a new entry
    // is chosen (or the drawer reopens).
    setPorts(
      entry.ports.map((p) => ({
        name: p.name,
        enabled: p.default_enabled,
        bind_interface: p.default_bind,
        // For public-bound ports we suggest matching the container
        // port externally (the common case: SSH 22 -> 22, SMTP 25 ->
        // 25, etc). For loopback ports nginx terminates so leave 0 =
        // auto-pick a free high port at install time.
        host_port: p.default_bind === "public" ? p.container_port : 0,
        reverse_proxy: p.default_reverse_proxy,
        container_port: p.container_port,
        protocol: p.protocol,
      })),
    );
    form.resetFields();
    form.setFieldsValue({
      slug: entry.slug,
      cpu_limit: entry.resources.cpu,
      memory_limit: entry.resources.memory,
      pids_limit: entry.resources.pids,
      update_mode: entry.update_mode,
    });
  }, [entry, form]);

  const install = useMutation({
    mutationFn: async (req: InstallRequest) => installApp(req),
    onSuccess: () => {
      message.success("Install dispatched");
      qc.invalidateQueries({ queryKey: ["docker-apps-installed"] });
      onClose();
      onInstalled?.();
    },
    onError: (e: unknown) =>
      message.error(
        e instanceof Error
          ? e.message
          : "Install failed; see server logs",
      ),
  });

  const handleFinish = (values: { slug: string; name: string; domain?: string; update_mode: "manual" | "auto"; cpu_limit: string; memory_limit: string; pids_limit?: number; backup_destination_id?: string }) => {
    if (!entry) return;
    install.mutate({
      slug: entry.slug,
      name: values.name,
      domain: values.domain,
      update_mode: values.update_mode,
      cpu_limit: values.cpu_limit,
      memory_limit: values.memory_limit,
      pids_limit: values.pids_limit,
      ports: ports.map(({ container_port: _cp, protocol: _proto, ...rest }) => rest),
      env: Object.fromEntries(Object.entries(envVals).filter(([, v]) => v !== "")),
      ...(values.backup_destination_id
        ? { backup_destination_id: values.backup_destination_id }
        : {}),
    });
  };

  const portsTable = useMemo(
    () => (
      <Table<PortRow>
        size="small"
        rowKey="name"
        dataSource={ports}
        pagination={false}
        columns={[
          { title: "Port", dataIndex: "name", width: 100 },
          {
            title: "Container",
            render: (_, row) => `${row.container_port}/${row.protocol}`,
            width: 100,
          },
          {
            title: "Enabled",
            dataIndex: "enabled",
            width: 90,
            render: (_, row) => (
              <Switch
                size="small"
                checked={row.enabled}
                onChange={(v) =>
                  setPorts((s) => s.map((p) => (p.name === row.name ? { ...p, enabled: v } : p)))
                }
              />
            ),
          },
          {
            title: "Bind",
            dataIndex: "bind_interface",
            width: 130,
            render: (_, row) => (
              <Select
                size="small"
                value={row.bind_interface}
                style={{ width: "100%" }}
                onChange={(v) =>
                  setPorts((s) => s.map((p) => (p.name === row.name ? { ...p, bind_interface: v } : p)))
                }
                options={[
                  { value: "loopback", label: "loopback" },
                  { value: "public", label: "public IP" },
                ]}
              />
            ),
          },
          {
            title: "Host port",
            dataIndex: "host_port",
            width: 120,
            render: (_, row) => (
              <InputNumber
                size="small"
                placeholder="auto"
                value={row.host_port || undefined}
                min={1024}
                max={65535}
                onChange={(v) =>
                  setPorts((s) => s.map((p) => (p.name === row.name ? { ...p, host_port: v ?? 0 } : p)))
                }
              />
            ),
          },
          {
            title: "Reverse proxy",
            dataIndex: "reverse_proxy",
            width: 110,
            render: (_, row) => (
              <Switch
                size="small"
                checked={row.reverse_proxy}
                disabled={row.bind_interface !== "loopback" || row.protocol === "udp"}
                onChange={(v) =>
                  setPorts((s) => s.map((p) => (p.name === row.name ? { ...p, reverse_proxy: v } : p)))
                }
              />
            ),
          },
        ]}
      />
    ),
    [ports, watchedDomain],
  );

  return (
    <Drawer
      open={open}
      onClose={onClose}
      title={entry ? `Install ${entry.name}` : "Install"}
      width="min(100%, 720px)"
      destroyOnClose
      extra={
        <Space>
          <Button onClick={onClose}>Cancel</Button>
          <Button type="primary" loading={install.isPending} onClick={() => form.submit()}>
            Install
          </Button>
        </Space>
      }
    >
      {entry && (
        <Form layout="vertical" form={form} onFinish={handleFinish}>
          <Alert
            type="info"
            message={`${entry.name} ${entry.version}`}
            description={entry.description}
            style={{ marginBottom: 16 }}
            showIcon
          />
          <Form.Item
            label={t("installdrawer.install_name")}
            name="name"
            rules={[
              { required: true, message: "Name is required" },
              { pattern: /^[a-z0-9-]{1,32}$/, message: "Lowercase letters, digits, dashes; up to 32 chars" },
              {
                validator: (_, value: string) => {
                  if (!value || !entry) return Promise.resolve();
                  const combined = `${entry.slug}-${value}`.length;
                  if (combined > 52) {
                    return Promise.reject(new Error(`Catalog slug + name exceeds 52 chars (got ${combined}). Pick a shorter name.`));
                  }
                  return Promise.resolve();
                },
              },
            ]}
            tooltip={t("installdrawer.per_install_identifier_combined_with_the_cat")}
          >
            <Input placeholder="e.g. vault-prod" />
          </Form.Item>
          <Form.Item
            label={t("installdrawer.domain")}
            name="domain"
            tooltip={t("installdrawer.hostname_to_attach_to_the_loopback_bound_por")}
          >
            <Select
              showSearch
              allowClear
              placeholder={t("installdrawer.select_a_domain")}
              optionFilterProp="label"
              options={(domainsQ.data ?? []).map((d) => ({ value: d.name, label: d.name }))}
              loading={domainsQ.isLoading}
            />
          </Form.Item>
          <Form.Item label={t("installdrawer.update_mode")} name="update_mode">
            <Select
              options={[
                { value: "manual", label: "Manual" },
                { value: "auto", label: "Auto + rollback" },
              ]}
            />
          </Form.Item>
          <Space size="middle" style={{ width: "100%" }}>
            <Form.Item label={t("installdrawer.cpu")} name="cpu_limit" style={{ flex: 1 }} extra={cpuCapNote}>
              <Input placeholder="0.5" />
            </Form.Item>
            <Form.Item label={t("installdrawer.memory")} name="memory_limit" style={{ flex: 1 }}>
              <Input placeholder="256m" />
            </Form.Item>
            <Form.Item label={t("installdrawer.pids")} name="pids_limit" style={{ flex: 1 }}>
              <InputNumber min={1} max={65535} style={{ width: "100%" }} />
            </Form.Item>
          </Space>

          <Form.Item
            label={t("installdrawer.backup_destination")}
            name="backup_destination_id"
            tooltip={t("installdrawer.pick_a_destination_from_server_settings_back")}
          >
            <Select
              allowClear
              placeholder={t("installdrawer.inherit_env_var_fallback")}
              options={(destsQ.data ?? []).map((d) => ({
                value: d.id,
                label: `${d.name} (${d.kind})`,
              }))}
              loading={destsQ.isLoading}
            />
          </Form.Item>

          <Typography.Title level={5} style={{ marginTop: 8 }}>
            Ports
          </Typography.Title>
          <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
            Catalog declares the universe. Disable ports you don't need. <Tag>Bind=loopback</Tag>
            puts the port behind nginx (TLS + CrowdSec). <Tag>Bind=public</Tag> binds to a panel-managed IP
            directly (UDP, SSH, etc).
          </Typography.Paragraph>
          {portsTable}

          {(entry.env ?? []).length > 0 && (
            <>
              <Typography.Title level={5} style={{ marginTop: 8 }}>
                Environment
              </Typography.Title>
              <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
                Secrets are auto-generated — view or change them later from Edit. Set any plain values here.
              </Typography.Paragraph>
              {(entry.env ?? []).map((ev) => {
                // Only generated vars are auto-filled. A secret WITHOUT a
                // generator (e.g. operator-supplied SMTP_PASSWORD, GH #322) is
                // operator input -> masked field, not "leave blank".
                const auto = !!ev.generate;
                const InputComp = ev.secret && !ev.generate ? Input.Password : Input;
                return (
                  <Form.Item
                    key={ev.name}
                    style={{ marginBottom: 8 }}
                    label={
                      <Space size={4}>
                        <Typography.Text code>{ev.name}</Typography.Text>
                        {auto && <Tag>auto-generated</Tag>}
                      </Space>
                    }
                  >
                    <InputComp
                      value={envVals[ev.name] ?? ""}
                      placeholder={auto ? "auto-generated (leave blank)" : ev.value ?? "optional"}
                      onChange={(e) => setEnvVals((st) => ({ ...st, [ev.name]: e.target.value }))}
                    />
                  </Form.Item>
                );
              })}
            </>
          )}
        </Form>
      )}
    </Drawer>
  );
};
