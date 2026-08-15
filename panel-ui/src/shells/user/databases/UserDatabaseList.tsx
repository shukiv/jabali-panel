// UserDatabaseList — tenant view of their databases with Quick Setup
// + Open-in-phpMyAdmin + Delete. phpMyAdmin SSO is wired through the
// apiClient helper; a blank tab is opened synchronously to dodge
// popup blockers while the SSO call runs.
// GH #1045: per-database Backup/Restore actions (download a one-shot
// dump; restore from an uploaded .sql file) — MariaDB only, the agent
// verbs shell out to mariadb-dump/mysql.
import { useTranslation } from "react-i18next";
import { useRef, useState, type ChangeEvent } from "react";
import { shortDateTime } from "../../../utils/datetime";
import { useQueryClient } from "@tanstack/react-query";
import { Button, Card, Space, Table, Typography } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
import {
  DatabaseOutlined,
  LinkOutlined,
  DeleteOutlined,
  PlusSquareOutlined,
  ThunderboltOutlined,
  DownloadOutlined,
  UploadOutlined,
} from "@icons";
import { sorterToParams } from "../../../utils/tableSorter";

import {
  downloadDatabaseBackup,
  restoreDatabaseUpload,
  ssoAdminer,
  ssoPhpMyAdmin,
} from "../../../apiClient";
import { EngineTag } from "../../../components/EngineTag";
import { RowActions } from "../../../components/RowActions";
import { columnSearchProps } from "../../../components/columnSearch";
import { SearchableTableStringQ } from "../../../components/SearchableTable";
import { useDeleteMutation } from "../../../hooks/useQueries";
import { useTableURL } from "../../../hooks/useTableURL";
import { QuickSetupModal } from "./QuickSetupModal";
import { UserDatabaseDrawer } from "./UserDatabaseDrawer";

export type Database = {
  id: string;
  user_id: string;
  name: string;
  engine: "mariadb" | "postgres";
  charset?: string;
  collation?: string;
  created_at: string;
  updated_at: string;
  size_bytes?: number;
};

const formatBytes = (bytes: number | undefined): string => {
  if (bytes === undefined || bytes === 0) return "0 B";

  const units = ["B", "KB", "MB", "GB", "TB"];
  let size = bytes;
  let unitIndex = 0;

  while (size >= 1024 && unitIndex < units.length - 1) {
    size /= 1024;
    unitIndex++;
  }

  if (unitIndex === 0) {
    return `${Math.floor(size)} B`;
  }
  return `${size.toFixed(1)} ${units[unitIndex]}`;
};

export const UserDatabaseList = () => {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const query = useTableURL<Database>({
    resource: "databases",
    defaultSort: "name",
    defaultOrder: "asc",
  });
  const deleteMutation = useDeleteMutation({ resource: "databases" });

  const [loadingAdminerId, setLoadingAdminerId] = useState<string | null>(null);
  const [loadingPhpMyAdminId, setLoadingPhpMyAdminId] = useState<string | null>(
    null,
  );
  const [loadingBackupId, setLoadingBackupId] = useState<string | null>(null);
  const [restoringId, setRestoringId] = useState<string | null>(null);
  const [quickSetupOpen, setQuickSetupOpen] = useState(false);
  const [createDrawerOpen, setCreateDrawerOpen] = useState(false);
  // Hidden file input for "Restore from file" — the row action only
  // records which database is the target and clicks this input; the
  // upload happens in its onChange. Kept as a ref (not state) because
  // the target row is only needed between the click and the change
  // event, never for rendering.
  const restoreInputRef = useRef<HTMLInputElement | null>(null);
  const restoreTargetRef = useRef<Database | null>(null);

  const handleTableChange: React.ComponentProps<
    typeof Table<Database>
  >["onChange"] = (pagination, _filters, sorter) => {
    const { sort, order } = sorterToParams<Database>(sorter);
    query.setParams({
      page: pagination.current ?? 1,
      pageSize: pagination.pageSize ?? 20,
      sort,
      order,
    });
  };

  const handleOpenPhpMyAdmin = async (row: Database) => {
    // Open a blank tab synchronously so it counts as a user-initiated
    // popup; most browsers block window.open() that fires after an
    // await. Opening with no features (not "noopener,noreferrer")
    // because `noopener` makes window.open return null — which then
    // falls into the else-branch below and navigates the CURRENT tab
    // while the blank tab stays open, orphaned. phpMyAdmin is served
    // same-origin from our nginx vhost, so window.opener access is
    // same-origin self-reference and not a cross-site threat anyway.
    // We still null out tab.opener after navigation as defense in
    // depth so the phpMyAdmin page can't reach back to navigate the
    // panel tab.
    const tab = window.open("", "_blank");
    try {
      setLoadingPhpMyAdminId(row.id);
      const response = await ssoPhpMyAdmin(row.id);
      if (tab) {
        tab.location.href = response.redirect_url;
        try {
          tab.opener = null;
        } catch {
          // ignore — some browsers treat opener as read-only
        }
      } else {
        window.location.assign(response.redirect_url);
      }
    } catch (error) {
      if (tab) tab.close();
      const errorMsg =
        error instanceof Error ? error.message : "Could not open phpMyAdmin";
      feedback.message.error(`Could not open phpMyAdmin: ${errorMsg}`);
    } finally {
      setLoadingPhpMyAdminId(null);
    }
  };

  const handleOpenAdminer = async (row: Database) => {
    // Same blank-tab-then-navigate dance as phpMyAdmin (popup blockers
    // require the window.open call to be synchronous with the click).
    const tab = window.open("", "_blank");
    try {
      setLoadingAdminerId(row.id);
      const response = await ssoAdminer(row.id);
      if (tab) {
        tab.location.href = response.redirect_url;
        try {
          tab.opener = null;
        } catch {
          // ignore — some browsers treat opener as read-only
        }
      } else {
        window.location.assign(response.redirect_url);
      }
    } catch (error) {
      if (tab) tab.close();
      const errorMsg =
        error instanceof Error ? error.message : "Could not open Adminer";
      feedback.message.error(`Could not open Adminer: ${errorMsg}`);
    } finally {
      setLoadingAdminerId(null);
    }
  };

  const handleDownloadBackup = async (row: Database) => {
    try {
      setLoadingBackupId(row.id);
      const { blob, filename } = await downloadDatabaseBackup(row.id);
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = filename;
      document.body.appendChild(a);
      a.click();
      document.body.removeChild(a);
      URL.revokeObjectURL(url);
    } catch (error) {
      const errorMsg =
        error instanceof Error ? error.message : "Backup failed";
      feedback.message.error(`Backup failed: ${errorMsg}`);
    } finally {
      setLoadingBackupId(null);
    }
  };

  const handlePickRestore = (row: Database) => {
    restoreTargetRef.current = row;
    restoreInputRef.current?.click();
  };

  const handleRestoreFile = async (
    e: ChangeEvent<HTMLInputElement>,
  ) => {
    const file = e.target.files?.[0];
    // Reset the input value so picking the same file twice in a row
    // still fires onChange the second time.
    e.target.value = "";
    const row = restoreTargetRef.current;
    if (!file || !row) return;
    try {
      setRestoringId(row.id);
      await restoreDatabaseUpload(row.id, file);
      feedback.message.success(
        `Database "${row.name}" restored from ${file.name}`,
      );
      // Size changed — refresh the listing.
      qc.invalidateQueries({ queryKey: ["list", "databases"] });
    } catch (error) {
      const errorMsg =
        error instanceof Error ? error.message : "Restore failed";
      feedback.message.error(`Restore failed: ${errorMsg}`);
    } finally {
      setRestoringId(null);
    }
  };

  return (
    <div>
      <input
        ref={restoreInputRef}
        type="file"
        accept=".sql"
        style={{ display: "none" }}
        onChange={(e) => {
          void handleRestoreFile(e);
        }}
      />
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
          <DatabaseOutlined /> Databases
        </Typography.Title>
        <Space wrap>
          <Button
            icon={<ThunderboltOutlined />}
            onClick={() => setQuickSetupOpen(true)}
          >
            Quick Setup
          </Button>
          <Button
            type="primary"
            icon={<PlusSquareOutlined />}
            onClick={() => setCreateDrawerOpen(true)}
          >
            Create Database
          </Button>
        </Space>
      </Space>

      <QuickSetupModal
        open={quickSetupOpen}
        onClose={() => setQuickSetupOpen(false)}
        onSuccess={() =>
          qc.invalidateQueries({ queryKey: ["list", "databases"] })
        }
      />

      <UserDatabaseDrawer
        open={createDrawerOpen}
        onClose={() => setCreateDrawerOpen(false)}
      />

      <Card>
        <SearchableTableStringQ<Database>
          rowKey="id"
          loading={query.isLoading}
          dataSource={query.items}
          initialSearch={query.params.q}
          searchPlaceholder="Search by database name"
          onSearchChange={(q) => query.setParams({ q, page: 1 })}
          pagination={{
            current: query.params.page,
            pageSize: query.params.pageSize,
            total: query.total,
          }}
          onChange={handleTableChange}
        >
          <Table.Column<Database>
            dataIndex="name"
            title={t("userdatabaselist.database")}
            key="name"
            sorter
            defaultSortOrder="ascend"
            {...columnSearchProps<Database>({
              placeholder: "Search by database name",
              currentQ: query.params.q,
              onSearch: (v) => query.setParams({ q: v, page: 1 }),
            })}
          />
          <Table.Column<Database>
            dataIndex="engine"
            title={t("userdatabaselist.engine")}
            width={140}
            render={(engine: string) => <EngineTag engine={engine} />}
          />
          <Table.Column<Database>
            dataIndex="size_bytes"
            title={t("userdatabaselist.size")}
            key="size_bytes"
            sorter={{ multiple: 3 }}
            render={(size_bytes?: number) => formatBytes(size_bytes)}
          />
          <Table.Column<Database>
            dataIndex="charset"
            title={t("userdatabaselist.charset")}
            render={(charset?: string) => charset || "-"}
          />
          <Table.Column<Database>
            dataIndex="created_at"
            title={t("userdatabaselist.created")}
            key="created_at"
            sorter
            render={(date: string) => shortDateTime(date)}
          />
          <Table.Column<Database>
            title={t("userdatabaselist.actions")}
            dataIndex="actions"
            render={(_, r) => {
              const isPostgres = r.engine === "postgres";
              const isLoading = loadingPhpMyAdminId === r.id;
              const isAdminerLoading = loadingAdminerId === r.id;

              const pmaAction = {
                key: "pma",
                label: "Open in phpMyAdmin",
                icon: <LinkOutlined />,
                onClick: () => handleOpenPhpMyAdmin(r),
                disabled: isPostgres || isLoading,
                loading: isLoading,
                tooltip: isPostgres ? "phpMyAdmin supports MySQL/MariaDB only" : undefined,
              };
              const adminerAction = {
                key: "adminer",
                label: "Open in Adminer",
                icon: <LinkOutlined />,
                onClick: () => handleOpenAdminer(r),
                disabled: isAdminerLoading,
                loading: isAdminerLoading,
              };
              // GH #1045 — per-database backup/restore. The agent verbs
              // shell out to mariadb-dump/mysql, so both are MariaDB-only
              // (same gate as phpMyAdmin).
              const mariaOnlyTooltip = isPostgres
                ? "Backup/restore supports MySQL/MariaDB only"
                : undefined;
              const backupAction = {
                key: "backup",
                label: "Download backup",
                icon: <DownloadOutlined />,
                onClick: () => void handleDownloadBackup(r),
                disabled: isPostgres || loadingBackupId === r.id,
                loading: loadingBackupId === r.id,
                tooltip: mariaOnlyTooltip,
              };
              const restoreAction = {
                key: "restore",
                label: "Restore from file",
                icon: <UploadOutlined />,
                onClick: () => handlePickRestore(r),
                disabled: isPostgres || restoringId === r.id,
                loading: restoringId === r.id,
                tooltip: mariaOnlyTooltip,
                confirm: {
                  title: `Restore into "${r.name}"?`,
                  description:
                    "The uploaded dump runs against this database — existing tables with the same names are overwritten.",
                  okText: "Choose .sql file",
                },
              };
              // Adminer is the native admin tool for Postgres (phpMyAdmin
              // is MariaDB-only and shows disabled), so lead with it there;
              // MariaDB keeps phpMyAdmin first. (GH #1005)
              const dbTools = isPostgres
                ? [adminerAction, pmaAction]
                : [pmaAction, adminerAction];

              return (
                <RowActions
                  actions={[
                    ...dbTools,
                    backupAction,
                    restoreAction,
                    {
                      key: "delete",
                      label: "Delete",
                      icon: <DeleteOutlined />,
                      danger: true,
                      onClick: () => { void deleteMutation.mutateAsync({ id: r.id }); },
                      confirm: { title: `Delete database "${r.name}"?`, okText: "Delete" },
                    },
                  ]}
                />
              );
            }}
          />
        </SearchableTableStringQ>
      </Card>
    </div>
  );
};
