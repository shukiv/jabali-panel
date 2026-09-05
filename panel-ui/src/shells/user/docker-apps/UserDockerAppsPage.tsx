// Tenant Docker Apps page (M49, GH #170). Catalog grid of tenant-installable
// apps + an install modal (loopback-only, attached to a domain you own) + the
// shared DockerAppInventory Module for the installed list. This shell supplies
// the tenant capability audience: loopback-only ports and NO
// exec / edit-compose / update / rebuild / backups — those verbs are absent
// from the audience, so the Module cannot render or dispatch them (ADR-0117 D8,
// JAB-335). Degrades to a clear "not enabled on this server" notice when the
// host has no userns-remap (the backend 403s docker_tenant_not_enabled).
import { useTranslation } from "react-i18next";
import { useMemo, useState } from "react";
import { CatalogCard, CatalogGrid, CategoryFilter } from "../../../components/catalog";
import { Alert, AutoComplete, Descriptions, Form, Input, Modal, Tabs, Tag, Typography } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
import { AppstoreOutlined } from "@icons";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { AxiosError } from "axios";
import { apiClient } from "../../../apiClient";
import { useTabParam } from "../../../hooks/useTabParam";

import type { CatalogEntry, InstalledApp } from "../../admin/docker-apps/types";
import {
  catalogIconUrl,
  deleteApp,
  fetchEnv,
  fetchLogs,
  fetchUsage,
  installApp,
  lifecycleAction,
  listCatalog,
  listInstalled,
} from "./api";
import type { EnvVarView } from "./api";
import { DockerAppInventory, type DockerInventoryAudience } from "../../../components/docker/DockerAppInventory";

export const UserDockerAppsPage = () => {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [installFor, setInstallFor] = useState<CatalogEntry | null>(null);
  const [logsFor, setLogsFor] = useState<InstalledApp | null>(null);
  const [credsFor, setCredsFor] = useState<InstalledApp | null>(null);

  const catalog = useQuery({ queryKey: ["user-docker-catalog"], queryFn: listCatalog });
  const [catalogTags, setCatalogTags] = useState<string[]>([]);
  const allCatalogTags = useMemo(() => {
    const set = new Set<string>();
    for (const e of catalog.data ?? []) for (const t of e.tags ?? []) set.add(t);
    return Array.from(set).sort();
  }, [catalog.data]);
  const visibleCatalog = useMemo(() => {
    const items = catalog.data ?? [];
    if (catalogTags.length === 0) return items;
    return items.filter((e) => (e.tags ?? []).some((t) => catalogTags.includes(t)));
  }, [catalog.data, catalogTags]);

  // Observed for the host-flag 403 (deduped with the Module's query by key).
  const installed = useQuery({ queryKey: ["user-docker-installed"], queryFn: listInstalled });
  const usage = useQuery({ queryKey: ["user-docker-usage"], queryFn: fetchUsage, retry: false });

  // The host-flag 403 surfaces on the installed-list / catalog query; treat it
  // as the "tenant docker disabled on this server" state.
  const disabled =
    (installed.error as AxiosError | undefined)?.response?.status === 403 ||
    (catalog.error as AxiosError | undefined)?.response?.status === 403;

  const [tab, setTab] = useTabParam<string>("installed");

  const audience: DockerInventoryAudience = {
    installedKey: ["user-docker-installed"],
    listInstalled,
    catalogCount: (catalog.data ?? []).length,
    catalogIconUrl,
    portPresentation: "loopback-only",
    lifecycle: lifecycleAction,
    remove: {
      label: "Delete",
      successMessage: "Deleted",
      confirm: (r) => ({
        title: `Delete "${r.name}"?`,
        description: "This removes the container and its data.",
        okText: "Delete",
      }),
      fn: deleteApp,
    },
    onLogs: (r) => setLogsFor(r),
    onCredentials: (r) => setCredsFor(r),
    // No privilegedActions / rowFilter: tenants get the constrained verb set and
    // see only their own installs.
    labels: {
      installedApps: "userdockerappspage.installed_apps",
      updatesAvailable: "userdockerappspage.updates_available",
      running: "userdockerappspage.running",
      stopped: "userdockerappspage.stopped",
      searchPlaceholder: "userdockerappspage.search_by_name",
      noAppsYet: "userdockerappspage.no_installed_apps_yet",
    },
    onBrowseCatalog: () => setTab("catalog"),
  };

  if (disabled) {
    return (
      <div>
        <Typography.Title level={3} style={{ marginTop: 0 }}>
          <AppstoreOutlined /> Docker Apps
        </Typography.Title>
        <Alert
          type="info"
          showIcon
          message={t("userdockerappspage.docker_apps_are_not_enabled_on_this_server")}
          description={t("userdockerappspage.ask_your_administrator_to_enable_tenant_dock")}
        />
      </div>
    );
  }

  return (
    <div>
      <Typography.Title level={3} style={{ marginTop: 0 }}>
        <AppstoreOutlined /> Docker Apps
      </Typography.Title>

      {usage.data?.over_quota && (
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 16 }}
          message={t("userdockerappspage.docker_apps_are_over_your_disk_quota")}
          description={t("userdockerappspage.your_installed_apps_exceed_your_package_disk")}
        />
      )}
      <Tabs
        activeKey={tab}
        onChange={setTab}
        items={[
          {
            key: "installed",
            label: "Installed",
            children: <DockerAppInventory audience={audience} />,
          },
          {
            key: "catalog",
            label: "Catalog",
            children: (
              <>
                <CategoryFilter tags={allCatalogTags} selected={catalogTags} onChange={setCatalogTags} />
                <CatalogGrid>
                  {visibleCatalog.map((e) => (
                    <CatalogCard
                      key={e.slug}
                      name={e.name}
                      iconUrl={catalogIconUrl(e.slug)}
                      meta={<Tag style={{ marginInlineEnd: 0 }}>v{e.version}</Tag>}
                      description={e.description}
                      onInstall={() => setInstallFor(e)}
                    />
                  ))}
                  {catalog.data?.length === 0 && (
                    <Typography.Text type="secondary">No apps available to install.</Typography.Text>
                  )}
                </CatalogGrid>
              </>
            ),
          },
        ]}
      />

      <LogsModal app={logsFor} onClose={() => setLogsFor(null)} />
      <CredentialsModal app={credsFor} onClose={() => setCredsFor(null)} />

      <InstallModal
        entry={installFor}
        onClose={() => setInstallFor(null)}
        onInstalled={() => {
          setInstallFor(null);
          qc.invalidateQueries({ queryKey: ["user-docker-installed"] });
        }}
      />
    </div>
  );
};

const InstallModal = ({
  entry,
  onClose,
  onInstalled,
}: {
  entry: CatalogEntry | null;
  onClose: () => void;
  onInstalled: () => void;
}) => {
  const [form] = Form.useForm<{ name: string; domain: string }>();
  // #281: suggest the user's owned domains; AutoComplete (not a plain Select)
  // keeps a free hostname typeable since the backend accepts a new subdomain
  // under the caller's ownership too.
  const domainsQuery = useQuery({
    queryKey: ["user-domains-min"],
    queryFn: async () => {
      const { data } = await apiClient.get<{ data: { id: string; name: string }[] }>("/domains", {
        params: { page: 1, page_size: 100 },
      });
      return data.data ?? [];
    },
  });
  const install = useMutation({
    mutationFn: (v: { name: string; domain: string }) =>
      installApp({ slug: entry!.slug, name: v.name, domain: v.domain }),
    onSuccess: () => {
      feedback.message.success("Install started");
      form.resetFields();
      onInstalled();
    },
    onError: (e: AxiosError<{ detail?: string; error?: string }>) =>
      feedback.message.error(`Install failed: ${e.response?.data?.detail || e.response?.data?.error}`),
  });

  return (
    <Modal
      open={!!entry}
      title={entry ? `Install ${entry.name}` : ""}
      onCancel={onClose}
      okText="Install"
      confirmLoading={install.isPending}
      onOk={() => form.validateFields().then((v) => install.mutate(v))}
    >
      <Form form={form} layout="vertical">
        <Form.Item
          name="name"
          label="Install name"
          rules={[
            { required: true, message: "Required" },
            { pattern: /^[a-z0-9-]{1,32}$/, message: "lowercase letters, digits, hyphens" },
          ]}
        >
          <Input placeholder="my-notes" />
        </Form.Item>
        <Form.Item
          name="domain"
          label="Domain"
          extra="A domain you own (or a new hostname). The app is served here over HTTPS."
          rules={[{ required: true, message: "Required" }]}
        >
          <AutoComplete
            placeholder="Select a domain you own, or type a new hostname"
            options={(domainsQuery.data ?? []).map((d) => ({ value: d.name }))}
            filterOption={(input, opt) => (opt?.value ?? "").toLowerCase().includes(input.toLowerCase())}
          />
        </Form.Item>
      </Form>
    </Modal>
  );
};

const LogsModal = ({ app, onClose }: { app: InstalledApp | null; onClose: () => void }) => {
  const q = useQuery({
    queryKey: ["user-docker-logs", app?.id],
    queryFn: () => fetchLogs(app!.id),
    enabled: !!app,
  });
  return (
    <Modal open={!!app} title={app ? `Logs — ${app.name}` : ""} onCancel={onClose} onOk={onClose} width={760} footer={null}>
      {q.isLoading ? (
        "Loading…"
      ) : (
        <pre
          style={{
            maxHeight: 460,
            overflow: "auto",
            whiteSpace: "pre-wrap",
            wordBreak: "break-word",
            margin: 0,
            fontSize: 12,
          }}
        >
          {q.data?.logs || "(no output)"}
        </pre>
      )}
    </Modal>
  );
};

const CredentialsModal = ({ app, onClose }: { app: InstalledApp | null; onClose: () => void }) => {
  const q = useQuery({
    queryKey: ["user-docker-env", app?.id],
    queryFn: () => fetchEnv(app!.id),
    enabled: !!app,
  });
  return (
    <Modal open={!!app} title={app ? `Credentials — ${app.name}` : ""} onCancel={onClose} onOk={onClose} width={640} footer={null}>
      <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
        Generated secrets for this install (admin password, DB password, keys).
      </Typography.Paragraph>
      <Descriptions size="small" column={1} bordered>
        {(q.data ?? []).map((e: EnvVarView) => (
          <Descriptions.Item key={e.name} label={e.name}>
            <Typography.Text copyable={!!e.value} code>
              {e.value || "—"}
            </Typography.Text>
          </Descriptions.Item>
        ))}
      </Descriptions>
    </Modal>
  );
};
