// User-shell Applications page — lists every install (WordPress
// today, DokuWiki/MediaWiki/etc. as M19 lands them). Post-M21:
// useTableURL with a custom useQuery `refetchInterval` so
// transitional statuses (pending/installing/cloning/deleting) poll
// until ready.
import { useTranslation } from "react-i18next";
import { useEffect, useState } from "react";
import { shortDateTime } from "../../../utils/datetime";
import { useTabParam } from "../../../hooks/useTabParam";
import {
  Button,
  Tabs,
  Col,
  Empty,
  Row,
  Space,
  Switch,
  Table,
  Tag,
  Typography,
  message,
  Tooltip,
} from "antd";
import {
  AppstoreOutlined,
  PlusSquareOutlined,
  ImportOutlined,
  LoadingOutlined,
  CheckCircleOutlined,
  ExclamationCircleOutlined,
  SyncOutlined,
  DeleteOutlined,
  CopyOutlined,
  LoginOutlined,
  SearchOutlined,
  ThunderboltOutlined,
  SettingOutlined,
} from "@icons";
import { useQueryClient } from "@tanstack/react-query";
import type { SorterResult } from "antd/es/table/interface";

import { columnSearchProps } from "../../../components/columnSearch";
import { RowActions } from "../../../components/RowActions";
import { SearchableTableStringQ } from "../../../components/SearchableTable";
import { apiClient } from "../../../apiClient";
import { useAuth } from "../../../auth/AuthContext";
import { useTableURL } from "../../../hooks/useTableURL";
import { useMagicLink } from "../../../hooks/useMagicLink";
import { InstallApplicationModal } from "./InstallApplicationModal";
import { MigrateRemoteDrawer } from "./MigrateRemoteDrawer";
import { CloneApplicationModal } from "./CloneApplicationModal";
import { CacheSettingsDrawer } from "./CacheSettingsDrawer";
import { CatalogTab } from "./CatalogTab";
import { CmsIcon, appDisplayName } from "./CmsIcon";
import { useAppRegistry } from "./appRegistry";
import { StatCard } from "../../../components/StatCard";

type ApplicationInstall = {
  id: string;
  app_type?: string;
  domain_id: string;
  domain_name: string;
  db_id: string;
  admin_username: string;
  admin_email: string;
  locale: string;
  subdirectory: string;
  status:
    | "pending"
    | "installing"
    | "cloning"
    | "deleting"
    | "ready"
    | "failed";
  version: string | null;
  last_error: string;
  cache_enabled?: boolean;
  created_at: string;
  updated_at: string;
};

const STATUS_META: Record<
  ApplicationInstall["status"],
  { color: string; icon: React.ReactNode; label: string; spinning: boolean }
> = {
  pending:    { color: "default",    icon: <LoadingOutlined spin />,      label: "Pending",    spinning: true  },
  installing: { color: "processing", icon: <LoadingOutlined spin />,      label: "Installing", spinning: true  },
  cloning:    { color: "processing", icon: <LoadingOutlined spin />,      label: "Cloning",    spinning: true  },
  deleting:   { color: "warning",    icon: <LoadingOutlined spin />,      label: "Deleting",   spinning: true  },
  ready:      { color: "success",    icon: <CheckCircleOutlined />,       label: "Ready",      spinning: false },
  failed:     { color: "error",      icon: <ExclamationCircleOutlined />, label: "Failed",     spinning: false },
};

const TRANSITIONAL = new Set<ApplicationInstall["status"]>([
  "pending",
  "installing",
  "cloning",
  "deleting",
]);

interface ActionsCellProps {
  record: ApplicationInstall;
  isDeleting: boolean;
  canClone: boolean;
  canLogin: boolean;
  onClone: () => void;
  onDelete: () => void;
}

const ActionsCell = ({
  record,
  isDeleting,
  canClone,
  canLogin,
  onClone,
  onDelete,
}: ActionsCellProps) => {
  const { mint: mintMagicLink, loading: magicLinkLoading, error: magicLinkError } = useMagicLink(record.id);

  const handleMagicLink = async () => {
    try {
      const response = await mintMagicLink();
      window.open(
        response.url,
        "_blank",
        "noopener,noreferrer"
      );
      message.success("Admin login link opened");
    } catch {
      message.error(magicLinkError || "Failed to generate admin login link");
    }
  };

  const [purging, setPurging] = useState(false);
  const [warming, setWarming] = useState(false);
  const handlePurge = async () => {
    setPurging(true);
    try {
      await apiClient.post(`/domains/${record.domain_id}/cache/purge`);
      message.success("Page cache purged");
    } catch (err) {
      const msg =
        (err as { response?: { data?: { error?: string } } })?.response?.data
          ?.error ??
        (err as { message?: string })?.message ??
        "Purge failed";
      message.error(msg);
    } finally {
      setPurging(false);
    }
  };

  // GH #615: warm the page cache by crawling the site (fire-and-forget).
  const handleWarmup = async () => {
    setWarming(true);
    try {
      await apiClient.post(`/applications/${record.id}/cache-warmup`);
      message.success("Cache warmup started — crawling the site in the background");
    } catch (err) {
      const msg =
        (err as { response?: { data?: { error?: string } } })?.response?.data
          ?.error ??
        (err as { message?: string })?.message ??
        "Warmup failed";
      message.error(msg);
    } finally {
      setWarming(false);
    }
  };

  return (
    <RowActions
      actions={[
        {
          key: "login",
          label: "Log in to admin",
          icon: <LoginOutlined />,
          onClick: handleMagicLink,
          loading: magicLinkLoading,
          tooltip: "Log in to the admin dashboard",
          hidden: !canLogin,
        },
        {
          key: "purge",
          label: "Purge cache",
          icon: <ThunderboltOutlined />,
          onClick: handlePurge,
          loading: purging,
          hidden: !record.cache_enabled,
        },
        {
          key: "warmup",
          label: "Warm cache",
          icon: <ThunderboltOutlined />,
          onClick: handleWarmup,
          loading: warming,
          hidden: !record.cache_enabled,
        },
        {
          key: "clone",
          label: "Clone",
          icon: <CopyOutlined />,
          onClick: onClone,
          disabled: !canClone,
          tooltip: canClone ? undefined : "Clone is only available for healthy WordPress installs",
        },
        {
          key: "delete",
          label: "Delete",
          icon: <DeleteOutlined />,
          onClick: onDelete,
          danger: true,
          loading: isDeleting,
          confirm: {
            title: "Delete this application?",
            description: "The database, files, and any associated cron jobs will be removed. This cannot be undone.",
            okText: "Delete",
          },
        },
      ]}
    />
  );
};

export const UserApplicationList = () => {
  const { t } = useTranslation();
  const { user } = useAuth();
  const qc = useQueryClient();

  const tableQuery = useTableURL<ApplicationInstall>({
    resource: "applications",
    defaultSort: "domain_name",
    defaultOrder: "asc",
  });
  const registry = useAppRegistry();

  const [installOpen, setInstallOpen] = useState(false);
  const [migrateOpen, setMigrateOpen] = useState(false);
  const [presetAppType, setPresetAppType] = useState<string | undefined>(undefined);
  const [activeTab, setActiveTab] = useTabParam<string>("installed");
  const [cloneOpen, setCloneOpen] = useState(false);
  const [cloningId, setCloningId] = useState<string | null>(null);
  const [deletingId, setDeletingId] = useState<string | null>(null);
  const [cachingId, setCachingId] = useState<string | null>(null);
  const [cacheSettingsFor, setCacheSettingsFor] =
    useState<ApplicationInstall | null>(null);
  const [scanning, setScanning] = useState(false);

  const handleScan = async () => {
    setScanning(true);
    try {
      const res = await apiClient.post<{
        scanned: number;
        added: number;
        report?: Array<{
          domain: string;
          subdirectory: string;
          app_type: string;
          version?: string;
          action: string;
        }>;
      }>("/applications/scan");
      const { scanned, added } = res.data;
      if (added > 0) {
        message.success(
          `Found ${scanned} app${scanned === 1 ? "" : "s"}, registered ${added} new.`,
        );
      } else if (scanned > 0) {
        message.info(`Found ${scanned} app${scanned === 1 ? "" : "s"}, all already registered.`);
      } else {
        message.info("No applications found on disk.");
      }
      qc.invalidateQueries({ queryKey: ["list", "applications"] });
      tableQuery.refetch();
    } catch (err) {
      const msg =
        (err as { response?: { data?: { detail?: string; error?: string } } })
          ?.response?.data?.detail ??
        (err as { response?: { data?: { error?: string } } })?.response?.data
          ?.error ??
        (err as { message?: string })?.message ??
        "Scan failed";
      message.error(msg);
    } finally {
      setScanning(false);
    }
  };

  // Poll the list while any row is transitional (pending/installing/
  // cloning/deleting). Five-second cadence matches what Refine's old
  // refetchInterval returned. refetch identity is stable, so only
  // `active` triggers re-installing the timer.
  const hasTransitional = tableQuery.items.some((r) =>
    TRANSITIONAL.has(r.status),
  );
  useEffect(() => {
    if (!hasTransitional) return;
    const h = setInterval(() => {
      tableQuery.refetch();
    }, 5000);
    return () => clearInterval(h);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [hasTransitional]);

  const handleDelete = async (row: ApplicationInstall) => {
    setDeletingId(row.id);
    try {
      await apiClient.delete(`/applications/${row.id}`);
      message.success(`Deleting ${row.domain_name || row.domain_id}…`);
      qc.invalidateQueries({ queryKey: ["list", "applications"] });
      qc.invalidateQueries({ queryKey: ["list", "databases"] });
    } catch (err) {
      const msg =
        (err as {
          response?: { data?: { error?: string; detail?: string } };
          message?: string;
        })?.response?.data?.detail ??
        (err as { response?: { data?: { error?: string } } })?.response?.data
          ?.error ??
        (err as { message?: string })?.message ??
        "Delete failed";
      message.error(msg);
    } finally {
      setDeletingId(null);
    }
  };

  // #406: single switch -> Redis object cache (jabali-wp-cache plugin) + nginx
  // page cache for the app's domain. WordPress + ready only.
  const handleToggleCache = async (row: ApplicationInstall, enabled: boolean) => {
    setCachingId(row.id);
    try {
      await apiClient.put(`/applications/${row.id}/cache`, { enabled });
      message.success(
        enabled
          ? `Caching enabled for ${row.domain_name || row.domain_id}`
          : `Caching disabled for ${row.domain_name || row.domain_id}`,
      );
      qc.invalidateQueries({ queryKey: ["list", "applications"] });
    } catch (err) {
      const msg =
        (err as { response?: { data?: { error?: string } } })?.response?.data
          ?.error ??
        (err as { message?: string })?.message ??
        "Failed to toggle caching";
      message.error(msg);
    } finally {
      setCachingId(null);
    }
  };

  const handleTableChange: React.ComponentProps<
    typeof Table<ApplicationInstall>
  >["onChange"] = (pagination, _filters, sorter) => {
    const single = Array.isArray(sorter)
      ? (sorter[0] as SorterResult<ApplicationInstall> | undefined)
      : (sorter as SorterResult<ApplicationInstall>);
    tableQuery.setParams({
      page: pagination.current ?? 1,
      pageSize: pagination.pageSize ?? 20,
      sort: single?.columnKey ? String(single.columnKey) : undefined,
      order:
        single?.order === "ascend"
          ? "asc"
          : single?.order === "descend"
            ? "desc"
            : undefined,
    });
  };

  return (
    <div>
      <Space
        style={{
          marginBottom: 16,
          width: "100%",
          justifyContent: "space-between",
          flexWrap: "wrap",
          rowGap: 8,
        }}
      >
        <Typography.Title level={3} style={{ margin: 0 }}>
          <AppstoreOutlined /> Applications
        </Typography.Title>
        <Space wrap size={[8, 8]}>
          <Tooltip title={t("userapplicationlist.detect_wordpress_joomla_drupal_and_magento_i")}>
            <Button
              icon={<SearchOutlined />}
              loading={scanning}
              onClick={handleScan}
            >
              Scan
            </Button>
          </Tooltip>
          <Button
            icon={<ImportOutlined />}
            onClick={() => setMigrateOpen(true)}
          >
            Migrate from remote
          </Button>
          <Button
            type="primary"
            icon={<PlusSquareOutlined />}
            onClick={() => {
              setPresetAppType(undefined);
              setInstallOpen(true);
            }}
          >
            Install
          </Button>
        </Space>
      </Space>

      <MigrateRemoteDrawer
        open={migrateOpen}
        onClose={() => setMigrateOpen(false)}
        onSuccess={() => {
          tableQuery.refetch();
          setActiveTab("installed");
        }}
      />

      <InstallApplicationModal
        open={installOpen}
        presetAppType={presetAppType}
        onClose={() => {
          setInstallOpen(false);
          setPresetAppType(undefined);
        }}
        onSuccess={() => {
          tableQuery.refetch();
          setActiveTab("installed");
        }}
        defaultAdminEmail={user?.email}
      />

      <CloneApplicationModal
        open={cloneOpen}
        onClose={() => {
          setCloneOpen(false);
          setCloningId(null);
        }}
        onSuccess={() => tableQuery.refetch()}
        installId={cloningId ?? ""}
      />

      <CacheSettingsDrawer
        install={cacheSettingsFor}
        onClose={() => setCacheSettingsFor(null)}
      />

      {/* JAB-167: plain <Tabs>, matching the Docker Apps + Python Apps
          marketplaces (was a Card.tabList, which rendered a heavier card-header
          tab bar unlike the rest and a flat card container in dark). */}
      <Tabs
        activeKey={activeTab}
        onChange={setActiveTab}
        items={[
          {
            key: "installed",
            label: "Installed",
            children: (
              <div>
                {(() => {
                  const rows = tableQuery.items;
                  const installedCount = tableQuery.total;
                  const readyCount = rows.filter((r) => r.status === "ready").length;
                  const inProgressCount = rows.filter((r) => TRANSITIONAL.has(r.status)).length;
                  const failedCount = rows.filter((r) => r.status === "failed").length;
                  const catalogCount = registry.data?.length ?? 0;
                  const pct = (n: number) =>
                    rows.length > 0 ? Math.round((n / rows.length) * 100) : 0;
                  return (
                    <Row gutter={[16, 16]} style={{ marginBottom: 16 }}>
                      <Col xs={12} sm={12} lg={6}>
                        <StatCard
                          iconBg="rgba(207, 19, 34, 0.12)" iconColor="#cf1322" Icon={AppstoreOutlined}
                          label={t("userapplicationlist.installed_apps")} value={installedCount}
                          subtitle={<Typography.Text type="secondary">{catalogCount} in catalog</Typography.Text>}
                        />
                      </Col>
                      <Col xs={12} sm={12} lg={6}>
                        <StatCard
                          iconBg="rgba(63, 134, 0, 0.12)" iconColor="#3f8600" Icon={CheckCircleOutlined}
                          label={t("userapplicationlist.ready")} value={readyCount}
                          subtitle={<Typography.Text type="secondary">{pct(readyCount)}% of installed</Typography.Text>}
                        />
                      </Col>
                      <Col xs={12} sm={12} lg={6}>
                        <StatCard
                          iconBg="rgba(22, 119, 255, 0.12)" iconColor="#1677ff" Icon={SyncOutlined}
                          label={t("userapplicationlist.in_progress")} value={inProgressCount}
                          subtitle={inProgressCount > 0
                            ? <Typography.Text type="warning">Working…</Typography.Text>
                            : <Typography.Text type="secondary">Idle</Typography.Text>}
                        />
                      </Col>
                      <Col xs={12} sm={12} lg={6}>
                        <StatCard
                          iconBg="rgba(212, 107, 8, 0.12)" iconColor="#d46b08" Icon={ExclamationCircleOutlined}
                          label={t("userapplicationlist.failed")} value={failedCount}
                          subtitle={failedCount > 0
                            ? <Typography.Text type="danger">Needs attention</Typography.Text>
                            : <Typography.Text type="secondary">All healthy</Typography.Text>}
                        />
                      </Col>
                    </Row>
                  );
                })()}
        <SearchableTableStringQ<ApplicationInstall>
          rowKey="id"
          loading={tableQuery.isLoading}
          dataSource={tableQuery.items}
          initialSearch={tableQuery.params.q}
          searchPlaceholder="Search by domain"
          locale={{
            emptyText: (
              <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t("userapplicationlist.no_installed_apps_yet")}>
                <Button type="primary" onClick={() => setActiveTab("catalog")}>
                  Browse Catalog
                </Button>
              </Empty>
            ),
          }}
          onSearchChange={(q) => tableQuery.setParams({ q, page: 1 })}
          pagination={{
            current: tableQuery.params.page,
            pageSize: tableQuery.params.pageSize,
            total: tableQuery.total,
          }}
          onChange={handleTableChange}
        >
          <Table.Column<ApplicationInstall>
            dataIndex="domain_name"
            title={t("userapplicationlist.domain")}
            key="domain_name"
            sorter={{ multiple: 1 }}
            defaultSortOrder="ascend"
            {...columnSearchProps<ApplicationInstall>({
              placeholder: "Search by domain",
              currentQ: tableQuery.params.q,
              onSearch: (v) => tableQuery.setParams({ q: v, page: 1 }),
            })}
            render={(domainName: string, record) => {
              const base = domainName || record.domain_id;
              const path = record.subdirectory
                ? `/${record.subdirectory}/`
                : "/";
              const label = `${base}${path}`;
              const isLink = record.status === "ready" && !!domainName;
              const appKey = record.app_type || "wordpress";
              const meta = STATUS_META[record.status] ?? STATUS_META.pending;
              const statusTag = (
                <Tag color={meta.color} icon={meta.icon}>
                  {meta.label}
                </Tag>
              );
              const statusEl =
                record.status === "failed" && record.last_error ? (
                  <Tooltip title={record.last_error}>{statusTag}</Tooltip>
                ) : (
                  statusTag
                );
              return (
                <div style={{ display: "flex", alignItems: "flex-start", gap: 8 }}>
                  <CmsIcon appType={appKey} />
                  <div
                    style={{
                      display: "flex",
                      flexDirection: "column",
                      gap: 4,
                      alignItems: "flex-start",
                    }}
                  >
                    {isLink ? (
                      <a
                        href={`https://${domainName}${path}`}
                        target="_blank"
                        rel="noopener noreferrer"
                      >
                        {label}
                      </a>
                    ) : (
                      <span>{label}</span>
                    )}
                    {statusEl}
                  </div>
                </div>
              );
            }}
          />
          <Table.Column<ApplicationInstall>
            dataIndex="version"
            title={t("userapplicationlist.app_version")}
            responsive={["lg"]}
            render={(version: string | null, record) => (
              <Space size={6}>
                <Tag style={{ marginInlineEnd: 0 }}>{appDisplayName(record.app_type)}</Tag>
                <span>{version || "-"}</span>
              </Space>
            )}
          />
          <Table.Column<ApplicationInstall>
            dataIndex="admin_email"
            title={t("userapplicationlist.admin_email")}
            responsive={["md"]}
          />
          <Table.Column<ApplicationInstall>
            dataIndex="created_at"
            title={t("userapplicationlist.created")}
            responsive={["lg"]}
            key="created_at"
            sorter={{ multiple: 2 }}
            render={(date: string) => shortDateTime(date)}
          />
          <Table.Column<ApplicationInstall>
            title={t("userapplicationlist.cache")}
            dataIndex="cache_enabled"
            responsive={["md"]}
            render={(_, r) => {
              const supported =
                (r.app_type ?? "wordpress") === "wordpress" &&
                r.status === "ready";
              const sw = (
                <Switch
                  size="small"
                  checked={!!r.cache_enabled}
                  loading={cachingId === r.id}
                  disabled={!supported}
                  onChange={(checked) => handleToggleCache(r, checked)}
                />
              );
              if (!supported) return sw;
              return (
                <Space size={4}>
                  <Tooltip title={t("userapplicationlist.redis_object_cache_nginx_page_cache")}>
                    {sw}
                  </Tooltip>
                  <Tooltip title={t("userapplicationlist.cache_settings")}>
                    <Button
                      type="text"
                      size="small"
                      icon={<SettingOutlined />}
                      aria-label={t("userapplicationlist.cache_settings")}
                      onClick={() => setCacheSettingsFor(r)}
                    />
                  </Tooltip>
                </Space>
              );
            }}
          />
          <Table.Column<ApplicationInstall>
            title={t("userapplicationlist.actions")}
            dataIndex="actions"
            render={(_, r) => {
              const isDeleting =
                deletingId === r.id || r.status === "deleting";
              const appType = r.app_type ?? "wordpress";
              const canClone =
                r.status === "ready" && appType === "wordpress";
              // Admin login is implemented for WordPress, Drupal, and
              // Joomla — matches panel-api ssoAgentCommandFor. When
              // adding a new CMS to the SSO-file flow, widen this list.
              const canLogin =
                r.status === "ready" &&
                (appType === "wordpress" || appType === "drupal" || appType === "joomla");

              return (
                <ActionsCell
                  record={r}
                  isDeleting={isDeleting}
                  canClone={canClone}
                  canLogin={canLogin}
                  onClone={() => {
                    setCloningId(r.id);
                    setCloneOpen(true);
                  }}
                  onDelete={() => handleDelete(r)}
                />
              );
            }}
          />
        </SearchableTableStringQ>
              </div>
            ),
          },
          {
            key: "catalog",
            label: "Catalog",
            children: (
              <CatalogTab
                onInstall={(appType) => {
                  setPresetAppType(appType);
                  setInstallOpen(true);
                }}
              />
            ),
          },
        ]}
      />
    </div>
  );
};

