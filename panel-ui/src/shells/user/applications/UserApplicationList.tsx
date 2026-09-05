// User-shell Applications page — lists every install (WordPress
// today, DokuWiki/MediaWiki/etc. as M19 lands them). The install row,
// status badge, domain cell, transitional-poll rule, login capability,
// and delete + invalidation are shared with the admin list via
// components/applications (JAB-334); this shell keeps the tenant deltas:
// the install/scan/migrate/clone/cache actions, the StatCards, and the Tabs.
import { useTranslation } from "react-i18next";
import { useState } from "react";
import { shortDateTime } from "../../../utils/datetime";
import { useTabParam } from "../../../hooks/useTabParam";
import { Button, Tabs, Col, Empty, Row, Space, Switch, Table, Tag, Typography, Tooltip } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
import {
  AppstoreOutlined,
  PlusSquareOutlined,
  ImportOutlined,
  CheckCircleOutlined,
  ExclamationCircleOutlined,
  SyncOutlined,
  SearchOutlined,
  SettingOutlined,
} from "@icons";
import { useQueryClient } from "@tanstack/react-query";
import { isTransitionalStatus } from "../../../utils/applicationStatus";
import { sorterToParams } from "../../../utils/tableSorter";

import { columnSearchProps } from "../../../components/columnSearch";
import { RowActions } from "../../../components/RowActions";
import { SearchableTableStringQ } from "../../../components/SearchableTable";
import { ApplicationDomainCell } from "../../../components/applications/ApplicationDomainCell";
import { ApplicationStatusTag } from "../../../components/applications/ApplicationStatusTag";
import { buildApplicationActions } from "../../../components/applications/buildApplicationActions";
import { useTransitionalPoll } from "../../../components/applications/useTransitionalPoll";
import { useDeleteApplication } from "../../../components/applications/useDeleteApplication";
import {
  canApplicationLogin,
  openApplicationLogin,
  type ApplicationInstall,
} from "../../../components/applications/applicationInventory";
import { apiClient } from "../../../apiClient";
import { useAuth } from "../../../auth/AuthContext";
import { useTableURL } from "../../../hooks/useTableURL";
import { useMagicLink } from "../../../hooks/useMagicLink";
import { InstallApplicationModal } from "./InstallApplicationModal";
import { MigrateRemoteDrawer } from "./MigrateRemoteDrawer";
import { CloneApplicationModal } from "./CloneApplicationModal";
import { CacheSettingsDrawer } from "./CacheSettingsDrawer";
import { CatalogTab } from "./CatalogTab";
import { appDisplayName } from "./CmsIcon";
import { useAppRegistry } from "./appRegistry";
import { StatCard } from "../../../components/StatCard";

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
  const { mint, loading: loginLoading, error: loginError } = useMagicLink(record.id);

  const [purging, setPurging] = useState(false);
  const [warming, setWarming] = useState(false);
  const handlePurge = async () => {
    setPurging(true);
    try {
      await apiClient.post(`/domains/${record.domain_id}/cache/purge`);
      feedback.message.success("Page cache purged");
    } catch (err) {
      const msg =
        (err as { response?: { data?: { error?: string } } })?.response?.data
          ?.error ??
        (err as { message?: string })?.message ??
        "Purge failed";
      feedback.message.error(msg);
    } finally {
      setPurging(false);
    }
  };

  // GH #615: warm the page cache by crawling the site (fire-and-forget).
  const handleWarmup = async () => {
    setWarming(true);
    try {
      await apiClient.post(`/applications/${record.id}/cache-warmup`);
      feedback.message.success("Cache warmup started — crawling the site in the background");
    } catch (err) {
      const msg =
        (err as { response?: { data?: { error?: string } } })?.response?.data
          ?.error ??
        (err as { message?: string })?.message ??
        "Warmup failed";
      feedback.message.error(msg);
    } finally {
      setWarming(false);
    }
  };

  return (
    <RowActions
      actions={buildApplicationActions({
        canLogin,
        onLogin: () => openApplicationLogin(mint, loginError),
        loginLoading,
        onDelete,
        deleting: isDeleting,
        deleteDescription:
          "The database, files, and any associated cron jobs will be removed. This cannot be undone.",
        cacheEnabled: !!record.cache_enabled,
        onPurge: handlePurge,
        purging,
        onWarmup: handleWarmup,
        warming,
        canClone,
        onClone,
      })}
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
  const { deletingId, deleteApplication } = useDeleteApplication();
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
        feedback.message.success(
          `Found ${scanned} app${scanned === 1 ? "" : "s"}, registered ${added} new.`,
        );
      } else if (scanned > 0) {
        feedback.message.info(`Found ${scanned} app${scanned === 1 ? "" : "s"}, all already registered.`);
      } else {
        feedback.message.info("No applications found on disk.");
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
      feedback.message.error(msg);
    } finally {
      setScanning(false);
    }
  };

  // Poll the list while any row is transitional (pending/installing/
  // cloning/deleting) — shared rule with the admin list (JAB-334 AC1).
  useTransitionalPoll(tableQuery.items, tableQuery.refetch);

  // #406: single switch -> Redis object cache (jabali-wp-cache plugin) + nginx
  // page cache for the app's domain. WordPress + ready only.
  const handleToggleCache = async (row: ApplicationInstall, enabled: boolean) => {
    setCachingId(row.id);
    try {
      await apiClient.put(`/applications/${row.id}/cache`, { enabled });
      feedback.message.success(
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
      feedback.message.error(msg);
    } finally {
      setCachingId(null);
    }
  };

  const handleTableChange: React.ComponentProps<
    typeof Table<ApplicationInstall>
  >["onChange"] = (pagination, _filters, sorter) => {
    const { sort, order } = sorterToParams<ApplicationInstall>(sorter);
    tableQuery.setParams({
      page: pagination.current ?? 1,
      pageSize: pagination.pageSize ?? 20,
      sort,
      order,
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
                  const inProgressCount = rows.filter((r) => isTransitionalStatus(r.status)).length;
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
            sorter
            defaultSortOrder="ascend"
            {...columnSearchProps<ApplicationInstall>({
              placeholder: "Search by domain",
              currentQ: tableQuery.params.q,
              onSearch: (v) => tableQuery.setParams({ q: v, page: 1 }),
            })}
            render={(_domainName: string, record) => (
              <ApplicationDomainCell
                record={record}
                status={
                  <ApplicationStatusTag
                    status={record.status}
                    lastError={record.last_error}
                  />
                }
              />
            )}
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
            sorter
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
              const canClone =
                r.status === "ready" && (r.app_type ?? "wordpress") === "wordpress";
              return (
                <ActionsCell
                  record={r}
                  isDeleting={isDeleting}
                  canClone={canClone}
                  canLogin={canApplicationLogin(r)}
                  onClone={() => {
                    setCloningId(r.id);
                    setCloneOpen(true);
                  }}
                  onDelete={() => deleteApplication(r)}
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

