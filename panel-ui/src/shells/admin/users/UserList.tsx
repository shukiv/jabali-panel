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
import { Badge, Button, Card, Dropdown, Input, Segmented, Space, Table, Tag, Tooltip, Typography } from "antd";
import { DeleteOutlined, EditOutlined, MoreOutlined, PauseCircleOutlined, PlayCircleOutlined, SafetyOutlined, SearchOutlined, TeamOutlined } from "@icons";

import { RowActionButton } from "../../../components/RowActionButton";
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
import { UserSuspendAction } from "./UserSuspendAction";

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

const renderName = (_: unknown, r: User) =>
  [r.name_first, r.name_last].filter(Boolean).join(" ");

const renderCreated = (ts: string) => new Date(ts).toLocaleString();

// Shared row-action buttons for both tables. Wired to react-router
// directly — no <EditButton> wrapper around a plain <Button>.
//
// Button copy intentionally does NOT include the row's email: the
// users-spec E2E asserts on `getByRole("cell", { name: email })`, and
// if the action cell's accessible name contained the email too, the
// matcher would hit both cells and fail with a strict-mode violation.
function RowActions({
  user,
  onEdit,
}: {
  user: User;
  onEdit: (id: string) => void;
}) {
  const [reset2faOpen, setReset2faOpen] = useState(false);
  const [suspendOpen, setSuspendOpen] = useState(false);
  const [deleteOpen, setDeleteOpen] = useState(false);

  type MenuItem = NonNullable<
    NonNullable<Parameters<typeof Dropdown>[0]["menu"]>["items"]
  >[number];
  const items: MenuItem[] = [
    {
      key: "reset2fa",
      icon: <SafetyOutlined />,
      label: "Reset 2FA",
      onClick: () => setReset2faOpen(true),
    },
    ...(!user.is_admin
      ? [
          {
            key: "suspend",
            icon: user.suspended ? <PlayCircleOutlined /> : <PauseCircleOutlined />,
            label: user.suspended ? "Unsuspend" : "Suspend",
            danger: !user.suspended,
            onClick: () => setSuspendOpen(true),
          } as MenuItem,
        ]
      : []),
    { type: "divider" } as MenuItem,
    {
      key: "delete",
      icon: <DeleteOutlined />,
      label: "Delete",
      danger: true,
      onClick: () => setDeleteOpen(true),
    },
  ];

  return (
    <Space size="middle">
      <RowActionButton icon={<EditOutlined />} onClick={() => onEdit(user.id)}>
        Edit
      </RowActionButton>
      <Dropdown trigger={["click"]} placement="bottomRight" menu={{ items }}>
        <RowActionButton icon={<MoreOutlined />} color="default">
          Actions
        </RowActionButton>
      </Dropdown>
      <UserReset2FAAction
        userId={user.id}
        userEmail={user.email}
        open={reset2faOpen}
        onClose={() => setReset2faOpen(false)}
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
    </Space>
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
  const [suspendFilter, setSuspendFilter] = useState<"all" | "active" | "suspended">(
    "all",
  );
  const extraParams: Record<string, string> = { is_admin: String(isAdmin) };
  if (suspendFilter === "active") extraParams.suspended = "false";
  if (suspendFilter === "suspended") extraParams.suspended = "true";

  const query = useTableURL<User>({
    resource: "users",
    defaultSort: "created_at",
    defaultOrder: "desc",
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
            { label: "All", value: "all" },
            { label: "Active", value: "active" },
            { label: "Suspended", value: "suspended" },
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
            description={isAdmin ? "No administrators yet" : "No users yet"}
            ctaLabel={isAdmin ? "Create administrator" : "Create user"}
            onCta={onCreate}
          />
        ),
      }}
    >
      <Table.Column<User>
        dataIndex="email"
        title="Email"
        key="email"
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
        render={(email: string, r: User) =>
          r.suspended ? (
            <span>
              {email}{" "}
              <Tooltip
                title={
                  <>
                    <div>
                      <b>Suspended</b>
                      {r.suspended_at ? ` on ${new Date(r.suspended_at).toLocaleString()}` : ""}
                    </div>
                    {r.suspend_reason ? <div>Reason: {r.suspend_reason}</div> : null}
                  </>
                }
              >
                <Tag color="error">Suspended</Tag>
              </Tooltip>
            </span>
          ) : (
            email
          )
        }
      />
      <Table.Column<User>
        dataIndex="username"
        title="Username"
        key="username"
        render={(v: string | null | undefined) =>
          v ? (
            <Typography.Text style={{ fontFamily: "monospace" }}>
              {v}
            </Typography.Text>
          ) : (
            <Typography.Text type="secondary">—</Typography.Text>
          )
        }
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
      />
      <Table.Column
        title="Name"
        render={renderName}
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
      />
      {!isAdmin && (
        <Table.Column<User>
          title="Package"
          dataIndex="package_id"
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
        title="Created"
        key="created_at"
        sorter={{ multiple: 1 }}
        defaultSortOrder="descend"
        render={renderCreated}
      />
      {showDiskUsageColumn && (
        <Table.Column
          title="Disk usage"
          render={(_: unknown, r: User) => <UserDiskUsage userId={r.id} />}
        />
      )}
      <Table.Column
        title="Actions"
        dataIndex="actions"
        render={(_: unknown, r: User) => <RowActions user={r} onEdit={onEdit} />}
      />
    </SearchableTableStringQ>
    </div>
  );
}

export const UserList = () => {
  const [activeTab, setActiveTab] = useState<"users" | "admins">("users");
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
        <Button type="primary" onClick={openCreate}>
          Create
        </Button>
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
                <span>Users</span>
                <Badge count={usersCountQ.total} showZero color="#999" />
              </Space>
            ),
          },
          {
            key: "admins",
            tab: (
              <Space size={6}>
                <span>Administrators</span>
                <Badge count={adminsCountQ.total} showZero color="#999" />
              </Space>
            ),
          },
        ]}
        activeTabKey={activeTab}
        onTabChange={(k) => setActiveTab(k as "users" | "admins")}
      >
        {activeTab === "users" ? (
          <UsersShellTable
            isAdmin={false}
            searchPlaceholder="Search by email, username, or name"
            showDiskUsageColumn
            onEdit={openEdit}
            onCreate={openCreate}
          />
        ) : (
          <UsersShellTable
            isAdmin
            searchPlaceholder="Search by email or name"
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
