// EditDrawer — admin-only editor for an installed M48 docker-app.
// Covers the four limit fields (update_mode / cpu / mem / pids) plus
// the wire-level knobs (domain + per-port enabled/bind/host_port/
// reverse_proxy). Backend gates port + domain edits on `status =
// stopped` and re-renders the compose + re-dispatches the agent.
import { useTranslation } from "react-i18next";
import { Alert, App, Divider, Drawer, Form, Input, InputNumber, Select, Space, Switch, Table, Typography } from "antd";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useState } from "react";

import { apiClient } from "../../../apiClient";

import { useServerStatus } from "../../../hooks/useServerStatus";
import { listCatalog, patchApp } from "./api";
import { EnvSection } from "./EnvSection";
import type { CatalogEntry, InstallPortOverride, InstalledApp } from "./types";

interface Props {
  open: boolean;
  app: InstalledApp | null;
  onClose: () => void;
}

interface PortRow extends InstallPortOverride {
  container_port: number;
  protocol: "tcp" | "udp";
}

interface FormValues {
  update_mode: "manual" | "auto";
  cpu_limit?: string;
  memory_limit?: string;
  pids_limit?: number;
  domain?: string;
}

export const EditDrawer = ({ open, app, onClose }: Props) => {
  const { t } = useTranslation();
  const { message } = App.useApp();
  const qc = useQueryClient();
  const [form] = Form.useForm<FormValues>();
  const watchedDomain = Form.useWatch<string | undefined>("domain", form);
  // Mirror the install drawer: surface the agent's silent host-core cap
  // on the compose `cpus` limit so an edited value above the host count
  // reads as effective-at-N, not invisibly clamped (GH#178).
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

  const catalogQ = useQuery({ queryKey: ["docker-apps-catalog"], queryFn: listCatalog });
  const entry: CatalogEntry | undefined = useMemo(
    () => (catalogQ.data ?? []).find((e) => e.slug === app?.slug),
    [catalogQ.data, app?.slug],
  );

  const domainsQ = useQuery({
    queryKey: ["admin-domains-for-docker-app"],
    queryFn: async () => {
      const { data } = await apiClient.get<{ data?: { id: string; name: string }[] }>(
        "/domains?pageSize=500",
      );
      return data.data ?? [];
    },
  });

  useEffect(() => {
    if (!app || !entry) return;
    form.setFieldsValue({
      update_mode: (app.update_mode as "manual" | "auto") ?? "manual",
      cpu_limit: app.cpu_limit ?? "",
      memory_limit: app.memory_limit ?? "",
      pids_limit: app.pids_limit ?? undefined,
      domain: app.domain ?? undefined,
    });
    // Seed ports from the catalog universe, hydrated with the
    // current install's bindings.
    const byName = new Map(app.ports.map((p) => [p.port_name, p]));
    setPorts(
      entry.ports.map((p) => {
        const cur = byName.get(p.name);
        return {
          name: p.name,
          enabled: cur ? cur.enabled : p.default_enabled,
          bind_interface: cur ? cur.bind_interface : p.default_bind,
          host_port: cur ? cur.host_port : 0,
          reverse_proxy: cur ? cur.reverse_proxy : p.default_reverse_proxy,
          container_port: p.container_port,
          protocol: p.protocol,
        };
      }),
    );
  }, [app, entry, form]);

  const patch = useMutation({
    mutationFn: async (values: FormValues) => {
      if (!app) return;
      await patchApp(app.id, {
        update_mode: values.update_mode,
        cpu_limit: values.cpu_limit || undefined,
        memory_limit: values.memory_limit || undefined,
        pids_limit: values.pids_limit ?? undefined,
        domain: values.domain ?? "",
        ports: ports.map(({ container_port: _cp, protocol: _proto, ...rest }) => rest),
      });
    },
    onSuccess: () => {
      message.success("Saved");
      qc.invalidateQueries({ queryKey: ["docker-apps-installed"] });
      onClose();
    },
    onError: (e: unknown) => message.error(e instanceof Error ? e.message : "Save failed"),
  });

  const running = app?.status === "running";

  const portsTable = useMemo(
    () => (
      <Table<PortRow>
        size="small"
        rowKey="name"
        pagination={false}
        dataSource={ports}
        columns={[
          { title: "Port", dataIndex: "name", width: 80 },
          {
            title: "Container",
            width: 110,
            render: (_, row) => `${row.container_port}/${row.protocol}`,
          },
          {
            title: "Enabled",
            width: 80,
            render: (_, row) => {
              const managedByDomain = row.name === "http" && !!watchedDomain;
              return (
                <Switch
                  size="small"
                  checked={managedByDomain ? true : (row.enabled ?? false)}
                  disabled={running || managedByDomain}
                  onChange={(v) =>
                    setPorts((s) => s.map((p) => (p.name === row.name ? { ...p, enabled: v } : p)))
                  }
                />
              );
            },
          },
          {
            title: "Bind",
            width: 140,
            render: (_, row) => {
              const managedByDomain = row.name === "http" && !!watchedDomain;
              return (
                <Select
                  size="small"
                  disabled={running || managedByDomain}
                  value={managedByDomain ? "loopback" : row.bind_interface}
                  style={{ width: "100%" }}
                  onChange={(v) =>
                    setPorts((s) => s.map((p) => (p.name === row.name ? { ...p, bind_interface: v } : p)))
                  }
                  options={[
                    { value: "loopback", label: "loopback" },
                    { value: "public", label: "public" },
                  ]}
                />
              );
            },
          },
          {
            title: "Host port",
            width: 110,
            render: (_, row) => {
              const managedByDomain = row.name === "http" && !!watchedDomain;
              return (
                <InputNumber
                  size="small"
                  min={0}
                  max={65535}
                  disabled={running || managedByDomain}
                  placeholder={managedByDomain ? "via domain" : "auto"}
                  style={{ width: "100%" }}
                  value={managedByDomain ? undefined : (row.host_port || undefined)}
                  onChange={(v) =>
                    setPorts((s) => s.map((p) => (p.name === row.name ? { ...p, host_port: v ?? 0 } : p)))
                  }
                />
              );
            },
          },
          {
            title: "Reverse proxy",
            width: 100,
            render: (_, row) => {
              const managedByDomain = row.name === "http" && !!watchedDomain;
              return (
                <Switch
                  size="small"
                  checked={managedByDomain ? true : (row.reverse_proxy ?? false)}
                  disabled={running || managedByDomain}
                  onChange={(v) =>
                    setPorts((s) => s.map((p) => (p.name === row.name ? { ...p, reverse_proxy: v } : p)))
                  }
                />
              );
            },
          },
        ]}
      />
    ),
    [ports, running, watchedDomain],
  );

  return (
    <Drawer
      title={app ? `Edit ${app.name}` : "Edit"}
      open={open}
      onClose={onClose}
      width="min(100%, 760px)"
      destroyOnHidden
      extra={
        <Space>
          <Typography.Text type="secondary">slug: {app?.slug}</Typography.Text>
        </Space>
      }
      footer={
        <Space style={{ width: "100%", justifyContent: "flex-end" }}>
          <a onClick={onClose}>Cancel</a>
          <a onClick={() => form.submit()}>Save</a>
        </Space>
      }
    >
      {running && (
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 16 }}
          message={t("editdrawer.stop_the_container_before_editing_ports_or_d")}
          description={t("editdrawer.limits_update_mode_can_still_be_saved_ports")}
        />
      )}
      <Form layout="vertical" form={form} onFinish={(v) => patch.mutate(v)}>
        <Form.Item label={t("editdrawer.update_mode")} name="update_mode">
          <Select
            options={[
              { value: "manual", label: "Manual" },
              { value: "auto", label: "Auto + rollback" },
            ]}
          />
        </Form.Item>

        <Form.Item label={t("editdrawer.domain")} name="domain" tooltip={t("editdrawer.pick_from_the_panel_s_domains_list_leave_emp")}>
          <Select
            showSearch
            allowClear
            disabled={running}
            placeholder={t("editdrawer.select_a_domain")}
            optionFilterProp="label"
            options={(domainsQ.data ?? []).map((d) => ({ value: d.name, label: d.name }))}
            loading={domainsQ.isLoading}
          />
        </Form.Item>

        <Space size="middle" style={{ width: "100%" }}>
          <Form.Item label={t("editdrawer.cpu")} name="cpu_limit" style={{ flex: 1 }} extra={cpuCapNote}>
            <Input placeholder="0.5" />
          </Form.Item>
          <Form.Item label={t("editdrawer.memory")} name="memory_limit" style={{ flex: 1 }}>
            <Input placeholder="256m" />
          </Form.Item>
          <Form.Item label={t("editdrawer.pids")} name="pids_limit" style={{ flex: 1 }}>
            <InputNumber min={1} max={65535} style={{ width: "100%" }} />
          </Form.Item>
        </Space>

        <Typography.Title level={5} style={{ marginTop: 8 }}>Ports</Typography.Title>
        <Typography.Paragraph type="secondary" style={{ marginBottom: 8 }}>
          Catalog declares the universe. Disable ports you don&apos;t need. <strong>Bind=loopback</strong> + <strong>reverse_proxy</strong>: nginx terminates TLS upstream. <strong>Bind=public</strong>: port binds 0.0.0.0 directly.
        </Typography.Paragraph>
        {portsTable}
      </Form>
      {app && (
        <>
          <Divider />
          <EnvSection appId={app.id} active={open} />
        </>
      )}
    </Drawer>
  );
};
