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
import { Badge, Button, Card, Input, message, Segmented, Space, Table, Tag, Tooltip, Typography } from "antd";
import { DeleteOutlined, EditOutlined, LoginOutlined, PauseCircleOutlined, PlayCircleOutlined, SafetyOutlined, SearchOutlined, TeamOutlined } from "@icons";
import { RowActions, type RowAction } from "../../../components/RowActions";
import { Link } from "react-router";
import { useTranslation } from "react-i18next";
import { adminLinks } from "../../../components/admin/entityLinks";
import { shortDateTime } from "../../../utils/datetime";

import { startImpersonation } from "../../../impersonation";
import type { SorterResult } from "antd/es/table/interface";

import { SearchableTableStringQ } from "../../../components/SearchableTable";
import { EmptyWithCTA } from "../../../components/EmptyWithCTA";
import { useListQuery } from "../../../hooks/useQueries";
import { useSelectQuery } from "../../../hooks/useSelectQuery";
import { useTableURL } from "../../../hooks/useTableURL";
import { UserDeleteAction } from "./UserDeleteAction";
import { UserDrawer } from "./UserDrawer";
import { UserDiskUsage } from "./UserDiskUsage";
import { UserReset2FAAction } from "./UserReset2FAAction";
import { UserResetPasswordAction } from "./UserResetPasswordAction";
import { UserSuspendAction } from "./UserSuspendAction";
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
  created_at: string;
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
  const [deleteOpen, setDeleteOpen] = useState(false);

  // ADR-0128 — start act-as, then full-reload into the user shell so /me
  // (now carrying the grant header) returns the target and every admin-scoped
  // query cache is dropped cleanly.
  const handleLoginAs = async () => {
    try {
      await startImpersonation(user.id);
      window.location.assign("/jabali-panel");
    } catch {
      message.error(t("users.error.login_as"));
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
        userEmail={user.email}
        open={reset2faOpen}
        onClose={() => setReset2faOpen(false)}
      />
      <UserResetPasswordAction
        userId={user.id}
        userEmail={user.email}
        open={resetPwOpen}
        onClose={() => setResetPwOpen(false)}
      />
      {!user.is_admin && (
        <UserSuspendAction
          userId={user.id}
          userEmail={user.email}
          suspended={!!user.suspended}
          open={suspendOpen}
          onClose={() => setSuspendOpen(false)}
        />
      )}
      <UserDeleteAction
        recordItemId={user.id}
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
    const single = Array.isArray(sorter)
      ? (sorter[0] as SorterResult<User> | undefined)
      : (sorter as SorterResult<User>);
    query.setParams({
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
        sorter={{ multiple: 1 }}
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
        sorter={{ multiple: 1 }}
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
          sorter={{ multiple: 1 }}
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
        sorter={{ multiple: 1 }}
        render={renderCreated}
      />
      {showDiskUsageColumn && (
        <Table.Column
          title={t("users.col.disk_usage")}
          render={(_: unknown, r: User) => <UserDiskUsage userId={r.id} />}
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
