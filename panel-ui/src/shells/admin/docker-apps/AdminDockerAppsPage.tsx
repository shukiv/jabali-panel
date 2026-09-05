// AdminDockerAppsPage — landing page for the M48 marketplace.
// Two tabs: Catalog (browse + install) and Installed (lifecycle), plus
// Maintenance. The Installed table body is the shared DockerAppInventory Module
// (JAB-335); this shell supplies the admin capability audience (bind-aware
// ports, the privileged edit / update / exec / backups verbs, owner scoping)
// and keeps the privileged drawers, the owner filter, and the catalog +
// maintenance tabs.
import { useTranslation } from "react-i18next";
import { App, Drawer, Space, Tabs, Tag, Typography } from "antd";
import { useTabParam } from "../../../hooks/useTabParam";
import { useMemo, useState } from "react";
import {
  ContainerOutlined,
  SyncOutlined,
  CodeOutlined,
  SaveOutlined,
  EditOutlined,
} from "@icons";
import type { RowAction } from "../../../components/RowActions";

import { deleteApp, lifecycleAction, listCatalog, listInstalled, updateApp } from "./api";
import type { CatalogEntry, InstalledApp } from "./types";
import { useSearchParams } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useOneQuery } from "../../../hooks/useQueries";
import { useSetBreadcrumbs } from "../../../components/admin/BreadcrumbContext";
import { ownerResourceCrumbs, ownerLabel } from "../../../components/admin/entityLinks";
import { InstallDrawer } from "./InstallDrawer";
import { CatalogCard, CatalogGrid, CategoryFilter } from "../../../components/catalog";
import { LogsDrawer } from "./LogsDrawer";
import { ExecDrawer } from "./ExecDrawer";
import { BackupsDrawer } from "./BackupsDrawer";
import { EditDrawer } from "./EditDrawer";
import { EnvSection } from "./EnvSection";
import { MaintenanceTab } from "./MaintenanceTab";
import { DockerAppInventory, type DockerInventoryAudience } from "../../../components/docker/DockerAppInventory";
import { useThemeMode } from "../../../theme/ThemeModeContext";

export const AdminDockerAppsPage = () => {
  const { t } = useTranslation();
  const { message } = App.useApp();
  const { mode } = useThemeMode();
  const qc = useQueryClient();
  const [installEntry, setInstallEntry] = useState<CatalogEntry | null>(null);
  const [catalogTags, setCatalogTags] = useState<string[]>([]);
  const [logsAppId, setLogsAppId] = useState<string | null>(null);
  const [execAppId, setExecAppId] = useState<string | null>(null);
  const [credsAppId, setCredsAppId] = useState<string | null>(null);
  const [editApp, setEditApp] = useState<InstalledApp | null>(null);
  const [activeTab, setActiveTab] = useTabParam<string>("installed");
  const [backupsAppId, setBackupsAppId] = useState<string | null>(null);
  const [searchParams, setSearchParams] = useSearchParams();
  const ownerId = searchParams.get("user_id") ?? undefined;
  const ownerQ = useOneQuery<{ id: string; username?: string | null }>({
    resource: "users",
    id: ownerId,
    enabled: !!ownerId,
  });
  const clearOwner = () => {
    const next = new URLSearchParams(searchParams);
    next.delete("user_id");
    setSearchParams(next);
  };

  const catalog = useQuery({
    queryKey: ["docker-apps-catalog"],
    queryFn: listCatalog,
  });

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

  // Shared with the Module by cache key: the Module owns the installed query;
  // this observer (same key, deduped) resolves an app id -> name/last_error for
  // the log drawer.
  const installed = useQuery({ queryKey: ["docker-apps-installed"], queryFn: listInstalled });

  const updateImage = useMutation({
    mutationFn: async (id: string) => updateApp(id),
    onSuccess: () => {
      // Async: the server returned 202 (update started). The row shows the
      // "updating" spinner and the poll flips it to running/failed when the
      // background pull + recreate finishes. The click handler already toasted.
      qc.invalidateQueries({ queryKey: ["docker-apps-installed"] });
    },
    onError: (e: unknown) => message.error(e instanceof Error ? e.message : "Update failed"),
  });

  const ownerRef = ownerId ? { id: ownerId, username: ownerQ.data?.username } : undefined;

  useSetBreadcrumbs(
    ownerRef ? ownerResourceCrumbs(ownerRef, { key: "docker-apps", label: "Docker Apps" }) : null,
  );

  // Privileged verbs — supplied only by the admin audience. Their absence from
  // the tenant audience is what makes them unrenderable/undispatchable there.
  const privilegedActions = (r: InstalledApp): RowAction[] => [
    { key: "edit", label: "Edit", icon: <EditOutlined />, disabled: r.status === "running", onClick: () => setEditApp(r) },
    {
      key: "update",
      label: "Update",
      icon: <SyncOutlined />,
      // GH #794: catalog images are pinned to a reviewed digest, so a newer
      // version only appears when the catalog is bumped. Tell the operator
      // up-front why "Update" won't change the version when already current.
      onClick: () => {
        const hasUpdate = !!r.available_digest && r.available_digest !== (r.image_sha ?? "");
        message.info(
          hasUpdate
            ? "Update started — this can take a few minutes"
            : "Already on the latest catalog version — re-applying catalog config, but there's no newer version to pull yet. New app versions arrive when the catalog is bumped.",
        );
        updateImage.mutate(r.id);
      },
    },
    { key: "exec", label: "Exec", icon: <CodeOutlined />, onClick: () => setExecAppId(r.id) },
    { key: "backups", label: "Backups", icon: <SaveOutlined />, onClick: () => setBackupsAppId(r.id) },
  ];

  const audience: DockerInventoryAudience = {
    installedKey: ["docker-apps-installed"],
    listInstalled,
    catalogCount: (catalog.data ?? []).length,
    catalogIconUrl: (slug) => `/api/v1/admin/docker-apps/catalog/${slug}/icon?theme=${mode}`,
    portPresentation: "bind-aware",
    lifecycle: lifecycleAction,
    remove: {
      label: "Uninstall",
      successMessage: "Uninstalled",
      confirm: (r) => ({
        title: `Uninstall ${r.name}?`,
        description: "Volumes will be purged. This cannot be undone.",
        okText: "Uninstall",
      }),
      fn: (id) => deleteApp(id, false),
    },
    onLogs: (r) => setLogsAppId(r.id),
    onCredentials: (r) => setCredsAppId(r.id),
    privilegedActions,
    rowFilter: (r) => !ownerId || r.user_id === ownerId,
    labels: {
      installedApps: "admindockerappspage.installed_apps",
      updatesAvailable: "admindockerappspage.updates_available",
      running: "admindockerappspage.running",
      stopped: "admindockerappspage.stopped",
      searchPlaceholder: "admindockerappspage.search_by_name",
      noAppsYet: "admindockerappspage.no_installed_apps_yet",
    },
    onBrowseCatalog: () => setActiveTab("catalog"),
  };

  return (
    <div>
      <Space wrap align="center" style={{ marginBottom: 16 }}>
        <Typography.Title level={3} style={{ margin: 0 }}>
          <ContainerOutlined /> Docker Apps
        </Typography.Title>
        {ownerRef && (
          <Tag closable onClose={clearOwner} color="blue">
            Owner: {ownerLabel(ownerRef)}
          </Tag>
        )}
      </Space>

      <Tabs
        activeKey={activeTab}
        onChange={setActiveTab}
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
                      iconUrl={`/api/v1/admin/docker-apps/catalog/${e.slug}/icon?theme=${mode}`}
                      meta={<Tag style={{ marginInlineEnd: 0 }}>v{e.version}</Tag>}
                      description={e.description}
                      onInstall={() => setInstallEntry(e)}
                    />
                  ))}
                </CatalogGrid>
              </>
            ),
          },
          {
            key: "maintenance",
            label: "Maintenance",
            children: <MaintenanceTab />,
          },
        ]}
      />

      <InstallDrawer
        open={installEntry !== null}
        entry={installEntry}
        onClose={() => setInstallEntry(null)}
        onInstalled={() => setActiveTab("installed")}
      />
      <LogsDrawer
        open={logsAppId !== null}
        appId={logsAppId}
        appName={(installed.data ?? []).find((a) => a.id === logsAppId)?.name}
        lastError={(installed.data ?? []).find((a) => a.id === logsAppId)?.last_error}
        onClose={() => setLogsAppId(null)}
      />
      <ExecDrawer open={execAppId !== null} appId={execAppId} onClose={() => setExecAppId(null)} />
      <EditDrawer open={editApp !== null} app={editApp} onClose={() => setEditApp(null)} />
      <BackupsDrawer open={backupsAppId !== null} appId={backupsAppId} onClose={() => setBackupsAppId(null)} />
      <Drawer
        title={t("admindockerappspage.credentials")}
        open={credsAppId !== null}
        onClose={() => setCredsAppId(null)}
        width={760}
        destroyOnClose
      >
        {credsAppId && <EnvSection appId={credsAppId} active={credsAppId !== null} />}
      </Drawer>
    </div>
  );
};
