// Users + Administrators — split into two AntD card-style tabs. Each
// tab is its own useTableURL instance, scoped server-side via the
// hook's `extraParams.is_admin`. Tabs unmount inactive content by
// default (AntD), so the two useTableURL calls never run concurrently
// and their URL params don't collide either.
//
// Backend allowlist governs which columns are searchable/sortable;
// the ?is_admin filter is applied before search/sort so the paginated
// total stays correct per tab.
import { useState } from "react";
import { useTabParam } from "../../../hooks/useTabParam";
import { Badge, Button, Card, Input, Segmented, Space, Table, Tag, Tooltip, Typography } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
import { DeleteOutlined, EditOutlined, LoginOutlined, PauseCircleOutlined, PlayCircleOutlined, SafetyOutlined, SearchOutlined, TeamOutlined } from "@icons";
import { RowActions, type RowAction } from "../../../components/RowActions";
import { Link } from "react-router";
import { useTranslation } from "react-i18next";
import { adminLinks } from "../../../components/admin/entityLinks";
import { shortDateTime } from "../../../utils/datetime";

import { startImpersonation } from "../../../impersonation";
import { sorterToParams } from "../../../utils/tableSorter";

import { SearchableTableStringQ } from "../../../components/SearchableTable";
import { EmptyWithCTA } from "../../../components/EmptyWithCTA";
import { useListQuery } from "../../../hooks/useQueries";
import { useSelectQuery } from "../../../hooks/useSelectQuery";
import { useTableURL } from "../../../hooks/useTableURL";
import { UserDeleteAction } from "./UserDeleteAction";
import { userLabel } from "./userLabel";
import { UserDrawer } from "./UserDrawer";
import { UserDiskUsage, UserDiskUsageCell } from "./UserDiskUsage";
import { UserReset2FAAction } from "./UserReset2FAAction";
import { UserResetPasswordAction } from "./UserResetPasswordAction";
import { UserSuspendAction } from "./UserSuspendAction";
import { UserRenameAction } from "./UserRenameAction";
import { AdminSessionsPage } from "../sessions/AdminSessionsPage";

type User = {
  id: string;
  email: string;
  // POSIX account name for regular users; NULL/absent for admins.
  username?: string | null;
  name_first: string;
  name_last: string;
  is_admin: boolean;
  suspended?: boolean;
  suspended_at?: string | null;
  suspend_reason?: string;
  // Hosting package the user is provisioned against; NULL for admins.
  package_id?: string | null;
  // Disk-usage snapshot persisted by the sweeper so the column can be
  // sorted server-side. disk_checked_at absent = never swept yet, in which
  // case the cell falls back to its own /users/:id/usage fetch.
  disk_used_kb?: number;
  disk_limit_kb?: number;
  disk_checked_at?: string | null;
  created_at: string;
  // GH #1242: at-a-glance per-user roll-up (counts + this month's bandwidth).
  resources?: {
    domains: number;
    mailboxes: number;
    databases: number;
    docker_apps: number;
    backups: number;
    bandwidth_bytes: number;
  };
};

type HostingPackage = { id: string; name: string };

const renderCreated = (ts: string) => shortDateTime(ts);

// Shared row-action buttons for both tables. Wired to react-router
// directly — no <EditButton> wrapper around a plain <Button>.
//
// Button copy intentionally does NOT include the row's email: the
// users-spec E2E asserts on `getByRole("cell", { name: email })`, and
// if the action cell's accessible name contained the email too, the
// matcher would hit both cells and fail with a strict-mode violation.
// Compact byte formatter for the monthly-bandwidth line (GH #1242).
function fmtBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(0)} KB`;
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`;
  return `${(n / 1024 / 1024 / 1024).toFixed(2)} GB`;
}

function UserRowActions({
  user,
  onEdit,
}: {
  user: User;
  onEdit: (id: string) => void;
}) {
  const { t } = useTranslation();
  const [reset2faOpen, setReset2faOpen] = useState(false);
  const [resetPwOpen, setResetPwOpen] = useState(false);
  const [suspendOpen, setSuspendOpen] = useState(false);
  const [renameOpen, setRenameOpen] = useState(false);
  const [deleteOpen, setDeleteOpen] = useState(false);

  // ADR-0128 — start act-as, then full-reload into the user shell so /me
  // (now carrying the grant header) returns the target and every admin-scoped
  // query cache is dropped cleanly.
  const handleLoginAs = async () => {
    try {
      await startImpersonation(user.id);
      window.location.assign("/jabali-panel");
    } catch {
      feedback.message.error(t("users.error.login_as"));
    }
  };

  const actions: RowAction[] = [
    ...(!user.is_admin
      ? [
          {
            key: "login",
            label: t("users.actions.login_as"),
            icon: <LoginOutlined />,
            onClick: handleLoginAs,
          },
        ]
      : []),
    { key: "edit", label: t("users.actions.edit"), icon: <EditOutlined />, onClick: () => onEdit(user.id) },
    { key: "reset2fa", label: t("users.actions.reset_2fa"), icon: <SafetyOutlined />, onClick: () => setReset2faOpen(true) },
    { key: "resetpw", label: t("users.actions.reset_password"), icon: <SafetyOutlined />, onClick: () => setResetPwOpen(true) },
    ...(!user.is_admin
      ? [
          {
            key: "rename",
            label: t("users.actions.rename"),
            icon: <EditOutlined />,
            onClick: () => setRenameOpen(true),
          },
        ]
      : []),
    ...(!user.is_admin
      ? [
          {
            key: "suspend",
            label: user.suspended ? t("users.actions.unsuspend") : t("users.actions.suspend"),
            icon: user.suspended ? <PlayCircleOutlined /> : <PauseCircleOutlined />,
            danger: !user.suspended,
            onClick: () => setSuspendOpen(true),
          },
        ]
      : []),
    { key: "delete", label: t("users.actions.delete"), icon: <DeleteOutlined />, danger: true, onClick: () => setDeleteOpen(true) },
  ];

  return (
    <>
      <RowActions actions={actions} />
      <UserReset2FAAction
        userId={user.id}
        userLabel={userLabel(user)}
        open={reset2faOpen}
        onClose={() => setReset2faOpen(false)}
      />
      <UserResetPasswordAction
        userId={user.id}
        userLabel={userLabel(user)}
        open={resetPwOpen}
        onClose={() => setResetPwOpen(false)}
      />
      {!user.is_admin && (
        <UserSuspendAction
          userId={user.id}
          userLabel={userLabel(user)}
          suspended={!!user.suspended}
          open={suspendOpen}
          onClose={() => setSuspendOpen(false)}
        />
      )}
      {!user.is_admin && (
        <UserRenameAction
          userId={user.id}
          currentUsername={user.username}
          userLabel={userLabel(user)}
          open={renameOpen}
          onClose={() => setRenameOpen(false)}
        />
      )}
      <UserDeleteAction
        recordItemId={user.id}
        userLabel={userLabel(user)}
        username={user.username}
        userEmail={user.email}
        open={deleteOpen}
        onClose={() => setDeleteOpen(false)}
      />
    </>
  );
}

type UsersTableProps = {
  isAdmin: boolean;
  searchPlaceholder: string;
  showDiskUsageColumn: boolean;
  onEdit: (id: string) => void;
  onCreate: () => void;
};

function UsersShellTable({
  isAdmin,
  searchPlaceholder,
  showDiskUsageColumn,
  onEdit,
  onCreate,
}: UsersTableProps) {
  const { t } = useTranslation();
  const [suspendFilter, setSuspendFilter] = useState<"all" | "active" | "suspended">(
    "all",
  );
  const extraParams: Record<string, string> = { is_admin: String(isAdmin) };
  if (suspendFilter === "active") extraParams.suspended = "false";
  if (suspendFilter === "suspended") extraParams.suspended = "true";

  const query = useTableURL<User>({
    resource: "users",
    defaultSort: "username",
    defaultOrder: "asc",
    extraParams,
  });
  // Package lookup — single /packages list, reused across both tabs
  // via TanStack Query's cache. Admins don't have packages so the
  // column is only meaningful on the users tab, but keeping the call
  // here keeps the render paths identical. Skip the fetch on the
  // admins tab.
  const packagesQ = useSelectQuery<HostingPackage>({
    resource: "packages",
    labelField: "name",
    valueField: "id",
    enabled: !isAdmin,
  });
  const packageNameById = new Map(
    packagesQ.options.map((o) => [o.value, o.label]),
  );

  // AntD Table's onChange emits the current pagination + sorter;
  // project that back into useTableURL's params so the URL stays
  // the single source of truth.
  const handleTableChange: React.ComponentProps<typeof Table<User>>["onChange"] = (
    pagination,
    _filters,
    sorter,
  ) => {
    const { sort, order } = sorterToParams<User>(sorter);
    query.setParams({
      page: pagination.current ?? 1,
      pageSize: pagination.pageSize ?? 20,
      sort,
      order,
    });
  };

  return (
    <div>
      <div style={{ marginBottom: 12 }}>
        <Segmented<"all" | "active" | "suspended">
          options={[
            { label: t("users.filter.all"), value: "all" },
            { label: t("users.filter.active"), value: "active" },
            { label: t("users.filter.suspended"), value: "suspended" },
          ]}
          value={suspendFilter}
          onChange={(v) => {
            setSuspendFilter(v);
            query.setParams({ page: 1 });
          }}
        />
      </div>
    <SearchableTableStringQ<User>
      rowKey="id"
      loading={query.isLoading}
      dataSource={query.items}
      initialSearch={query.params.q}
      searchPlaceholder={searchPlaceholder}
      onSearchChange={(q) => query.setParams({ q, page: 1 })}
      pagination={{
        current: query.params.page,
        pageSize: query.params.pageSize,
        total: query.total,
      }}
      onChange={handleTableChange}
      locale={{
        emptyText: (
          <EmptyWithCTA
            description={isAdmin ? t("users.empty.admins") : t("users.empty.users")}
            ctaLabel={isAdmin ? t("users.cta.create_admin") : t("users.cta.create_user")}
            onCta={onCreate}
          />
        ),
      }}
    >
      <Table.Column<User>
        title={t("users.col.username")}
        dataIndex="username"
        key="username"
        sorter
        defaultSortOrder="ascend"
        render={(v: string | null | undefined, record: User) => (
          <Link to={adminLinks.user(record.id)} style={{ fontFamily: "monospace" }}>
            {v || record.id.substring(0, 8)}
          </Link>
        )}
      />
      <Table.Column<User>
        title={t("users.col.name")}
        key="name_first"
        sorter
        filterIcon={() => (
          <SearchOutlined
            style={{ color: query.params.q ? "#ef4444" : undefined }}
          />
        )}
        filterDropdown={({ confirm, close }) => (
          <div style={{ padding: 8, minWidth: 240 }}>
            <Input.Search
              placeholder={searchPlaceholder}
              allowClear
              defaultValue={query.params.q}
              onSearch={(value) => {
                query.setParams({ q: value.trim(), page: 1 });
                confirm({ closeDropdown: false });
                close();
              }}
            />
          </div>
        )}
        render={(_: unknown, r: User) => {
          const fullName = [r.name_first, r.name_last]
            .filter(Boolean)
            .join(" ");
          return (
            <div>
              <div>
                <Typography.Text>{fullName || "\u2014"}</Typography.Text>
                {r.suspended ? (
                  <Tooltip
                    title={
                      <>
                        <div>
                          <b>{t("users.suspended")}</b>
                          {r.suspended_at
                            ? ` ${t("users.suspended_on", { date: new Date(r.suspended_at).toLocaleString() })}`
                            : ""}
                        </div>
                        {r.suspend_reason ? (
                          <div>{t("users.suspend_reason", { reason: r.suspend_reason })}</div>
                        ) : null}
                      </>
                    }
                  >
                    <Tag color="error" style={{ marginLeft: 8 }}>
                      {t("users.suspended")}
                    </Tag>
                  </Tooltip>
                ) : null}
              </div>
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                {r.email}
              </Typography.Text>
            </div>
          );
        }}
      />
      {!isAdmin && (
        <Table.Column<User>
          title={t("users.col.package")}
          dataIndex="package_id"
          key="package_id"
          sorter
          render={(pid: string | null | undefined) => {
            if (!pid) return <Typography.Text type="secondary">—</Typography.Text>;
            const name = packageNameById.get(pid);
            return name ? (
              <Tag>{name}</Tag>
            ) : (
              <Typography.Text type="secondary">
                {pid.substring(0, 8)}…
              </Typography.Text>
            );
          }}
        />
      )}
      <Table.Column
        dataIndex="created_at"
        title={t("users.col.created")}
        key="created_at"
        sorter
        render={renderCreated}
      />
      {showDiskUsageColumn && (
        <Table.Column<User>
          title={t("users.col.disk_usage")}
          dataIndex="disk_used_kb"
          key="disk_used_kb"
          // Server-side sort (same form as the other columns): the sweeper
          // persists disk_used_kb, so the DB can ORDER BY it. Sorting the
          // per-row fetch was impossible — it only ever held the current
          // page's resolved rows.
          sorter
          render={(_: unknown, r: User) => (
            <div>
              {r.disk_checked_at ? (
                <UserDiskUsageCell usedKB={r.disk_used_kb ?? 0} limitKB={r.disk_limit_kb ?? 0} />
              ) : (
                // Never swept (fresh upgrade, or the sweeper has not reached
                // this row yet) — fall back to the live per-row fetch so the
                // column is never blank.
                <UserDiskUsage userId={r.id} />
              )}
              {r.resources && r.resources.bandwidth_bytes > 0 && (
                <Tooltip title={t("users.col.bandwidth_month")}>
                  <Typography.Text
                    type="secondary"
                    style={{ fontSize: 11, whiteSpace: "nowrap", display: "block" }}
                  >
                    ↕ {fmtBytes(r.resources.bandwidth_bytes)} /mo
                  </Typography.Text>
                </Tooltip>
              )}
            </div>
          )}
        />
      )}
      {showDiskUsageColumn && (
      <Table.Column<User>
        title={t("users.col.resources")}
        key="resources"
        render={(_: unknown, r: User) => {
          const R = r.resources;
          if (!R) return <Typography.Text type="secondary">—</Typography.Text>;
          const chips: Array<[string, string, number]> = [
            [t("users.res.domains"), "Dom", R.domains],
            [t("users.res.mailboxes"), "Mbx", R.mailboxes],
            [t("users.res.databases"), "DB", R.databases],
            [t("users.res.docker"), "Dkr", R.docker_apps],
            [t("users.res.backups"), "Bkp", R.backups],
          ];
          return (
            <Space size={8} wrap>
              {chips.map(([full, abbr, n]) => (
                <Tooltip key={abbr} title={full}>
                  <span style={{ fontSize: 12, whiteSpace: "nowrap" }}>
                    <Typography.Text type="secondary">{abbr}</Typography.Text> {n}
                  </span>
                </Tooltip>
              ))}
            </Space>
          );
        }}
      />
      )}
      <Table.Column
        title={t("users.col.actions")}
        dataIndex="actions"
        render={(_: unknown, r: User) => <UserRowActions user={r} onEdit={onEdit} />}
      />
    </SearchableTableStringQ>
    </div>
  );
}

export const UserList = () => {
  const { t } = useTranslation();
  const [activeTab, setActiveTab] = useTabParam<"users" | "admins" | "sessions">(
    "users",
  );
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [editingId, setEditingId] = useState<string | undefined>(undefined);

  const openCreate = () => {
    setEditingId(undefined);
    setDrawerOpen(true);
  };
  const openEdit = (id: string) => {
    setEditingId(id);
    setDrawerOpen(true);
  };
  const closeDrawer = () => setDrawerOpen(false);

  // Tab-label badges need totals for BOTH roles regardless of which
  // tab is active. Tabs unmount inactive content, so the per-tab
  // useTableURL can't tell us the inactive count — fetch each total
  // here with a pageSize=1 list so the payload is just the count +
  // one row.
  const usersCountQ = useListQuery<User>({
    resource: "users",
    params: { pageSize: 1, is_admin: "false" },
  });
  const adminsCountQ = useListQuery<User>({
    resource: "users",
    params: { pageSize: 1, is_admin: "true" },
  });

  return (
    <div>
      <Space
        wrap
        align="center"
        style={{
          marginBottom: 16,
          width: "100%",
          justifyContent: "space-between",
        }}
      >
        <Typography.Title level={3} style={{ margin: 0 }}>
          <TeamOutlined /> Users
        </Typography.Title>
        {activeTab !== "sessions" && (
          <Button type="primary" onClick={openCreate}>
            Create
          </Button>
        )}
      </Space>

      {/* Card.tabList renders the tab strip visually attached to the
          card body — gives the connected "tab → panel" look the bare
          Tabs component lacks. activeTabKey drives which child table
          renders. */}
      <Card
        tabList={[
          {
            key: "users",
            tab: (
              <Space size={6}>
                <span>{t("users.tab.users")}</span>
                <Badge count={usersCountQ.total} showZero color="#999" />
              </Space>
            ),
          },
          {
            key: "admins",
            tab: (
              <Space size={6}>
                <span>{t("users.tab.admins")}</span>
                <Badge count={adminsCountQ.total} showZero color="#999" />
              </Space>
            ),
          },
          {
            // JAB-126: Sessions is now a tab here rather than a standalone
            // sidebar entry. The view itself (AdminSessionsPage) is unchanged.
            key: "sessions",
            tab: <span>{t("users.tab.sessions")}</span>,
          },
        ]}
        activeTabKey={activeTab}
        onTabChange={(k) =>
          setActiveTab(k as "users" | "admins" | "sessions")
        }
      >
        {activeTab === "sessions" ? (
          <AdminSessionsPage />
        ) : activeTab === "users" ? (
          <UsersShellTable
            isAdmin={false}
            searchPlaceholder={t("users.search.users")}
            showDiskUsageColumn
            onEdit={openEdit}
            onCreate={openCreate}
          />
        ) : (
          <UsersShellTable
            isAdmin
            searchPlaceholder={t("users.search.admins")}
            showDiskUsageColumn={false}
            onEdit={openEdit}
            onCreate={openCreate}
          />
        )}
      </Card>

      <UserDrawer open={drawerOpen} onClose={closeDrawer} editingId={editingId} />
    </div>
  );
};
