// Database Users list with grants embedded.
//
// Each row represents a MariaDB user (username + "@localhost"), and
// the "Database Privileges" column renders one tag per grant with an
// × that revokes just that grant. Row-level actions are Add Access
// (open AddGrantModal), Password (rotate + reveal modal), and Delete
// (drops the whole user, cascading all grants).
import { useTranslation } from "react-i18next";
import { useState } from "react";
import { shortDateTime } from "../../../utils/datetime";
import { Button, Card, Space, Table, Tag, Typography, message } from "antd";
import { KeyOutlined, PlusOutlined, UserOutlined, DeleteOutlined } from "@icons";
import { RowActions } from "../../../components/RowActions";
import { useQueryClient } from "@tanstack/react-query";

import { apiClient } from "../../../apiClient";
import { AddGrantModal } from "../../../components/AddGrantModal";
import { DatabaseUserPasswordModal } from "../../../components/DatabaseUserPasswordModal";
import { columnSearchProps } from "../../../components/columnSearch";
import { SearchableTableStringQ } from "../../../components/SearchableTable";
import { useDeleteMutation } from "../../../hooks/useQueries";
import { useTableURL } from "../../../hooks/useTableURL";
import { EngineTag } from "../../../components/EngineTag";
import { DatabaseUserDrawer } from "./DatabaseUserDrawer";

export type Grant = {
  id: string;
  database_id: string;
  database_name: string;
  grant_level: "rw" | "ro" | "custom";
  privileges?: string;
};

export type DatabaseUser = {
  id: string;
  user_id: string;
  username: string;
  engine: "mariadb" | "postgres";
  created_at: string;
  updated_at: string;
  grants: Grant[];
};

// Rotate-password calls require the client to POST something in
// new_password even though the server ignores it and generates its
// own. A local random string keeps the body valid.
function placeholderForRotateBody(): string {
  const alphabet =
    "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnpqrstuvwxyz23456789";
  const bytes = new Uint8Array(24);
  crypto.getRandomValues(bytes);
  let out = "";
  for (const b of bytes) out += alphabet[b % alphabet.length];
  return out;
}

function grantLabel(grant: Grant): string {
  switch (grant.grant_level) {
    case "rw":
      return "Full Access";
    case "ro":
      return "Read only";
    case "custom":
      if (grant.privileges) {
        const privs = grant.privileges.split(",").map((p) => p.trim());
        return privs.length > 2 ? `${privs.slice(0, 2).join(", ")}…` : privs.join(", ");
      }
      return "Custom";
    default:
      return "Unknown";
  }
}

export const DatabaseUsersList = () => {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const query = useTableURL<DatabaseUser>({
    resource: "database-users",
    defaultSort: "username",
    defaultOrder: "asc",
  });
  const deleteMutation = useDeleteMutation({ resource: "database-users" });

  const [passwordModal, setPasswordModal] = useState<{
    username: string;
    password: string;
    title: string;
  } | null>(null);
  const [rotatingId, setRotatingId] = useState<string | null>(null);
  const [revokingId, setRevokingId] = useState<string | null>(null);
  const [grantTarget, setGrantTarget] = useState<DatabaseUser | null>(null);
  const [createDrawerOpen, setCreateDrawerOpen] = useState(false);

  const refresh = () => {
    qc.invalidateQueries({ queryKey: ["list", "database-users"] });
  };

  const rotate = async (row: DatabaseUser) => {
    setRotatingId(row.id);
    try {
      const resp = await apiClient.post<{ password: string }>(
        `/database-users/${row.id}/rotate-password`,
        { new_password: placeholderForRotateBody() },
      );
      setPasswordModal({
        username: row.username,
        password: resp.data.password,
        title: "New password (rotation)",
      });
    } catch (err) {
      const msg =
        (err as { response?: { data?: { error?: string } } })?.response?.data
          ?.error ?? "Failed to rotate password";
      message.error(msg);
    } finally {
      setRotatingId(null);
    }
  };

  const revokeGrant = async (grant: Grant) => {
    setRevokingId(grant.id);
    try {
      await apiClient.delete(`/database-user-grants/${grant.id}`);
      message.success(
        `Revoked ${grantLabel(grant).toLowerCase()} on ${grant.database_name}`,
      );
      refresh();
    } catch (err) {
      const msg =
        (err as { response?: { data?: { error?: string } } })?.response?.data
          ?.error ?? "Failed to revoke grant";
      message.error(msg);
    } finally {
      setRevokingId(null);
    }
  };

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
          Database Users
        </Typography.Title>
        <Button
          type="primary"
          icon={<PlusOutlined />}
          onClick={() => setCreateDrawerOpen(true)}
        >
          Create User
        </Button>
      </Space>

      <Card>
        <SearchableTableStringQ<DatabaseUser>
          rowKey="id"
          loading={query.isLoading}
          dataSource={query.items}
          initialSearch={query.params.q}
          searchPlaceholder="Search by username"
          onSearchChange={(q) => query.setParams({ q, page: 1 })}
          pagination={{
            current: query.params.page,
            pageSize: query.params.pageSize,
            total: query.total,
            showSizeChanger: true,
            // GH #232: controlled pagination needs onChange or page/size
            // clicks are inert (current/pageSize are pinned to query state).
            onChange: (page, pageSize) => query.setParams({ page, pageSize }),
          }}
        >
          <Table.Column<DatabaseUser>
            dataIndex="username"
            title={t("databaseuserslist.user")}
            sorter={{ multiple: 1 }}
            defaultSortOrder="ascend"
            {...columnSearchProps<DatabaseUser>({
              placeholder: "Search by database user",
              currentQ: query.params.q,
              onSearch: (v) => query.setParams({ q: v, page: 1 }),
            })}
            render={(username: string) => (
              <Space>
                <UserOutlined />
                <span style={{ fontFamily: "monospace" }}>{username}</span>
                <Typography.Text type="secondary">@localhost</Typography.Text>
              </Space>
            )}
          />
          <Table.Column<DatabaseUser>
            dataIndex="engine"
            title={t("databaseuserslist.engine")}
            key="engine"
            sorter={{ multiple: 1 }}
            width={140}
            render={(engine: string | undefined) => (
              <EngineTag engine={engine} />
            )}
          />
          <Table.Column<DatabaseUser>
            title={t("databaseuserslist.database_privileges")}
            dataIndex="grants"
            render={(grants: Grant[] | undefined) => {
              if (!grants || grants.length === 0) {
                return (
                  <Typography.Text type="secondary">No grants</Typography.Text>
                );
              }
              return (
                <Space size={[4, 4]} wrap>
                  {grants.map((g) => (
                    <Tag
                      key={g.id}
                      color={
                        g.grant_level === "rw"
                          ? "green"
                          : g.grant_level === "ro"
                            ? "blue"
                            : "orange"
                      }
                      closable
                      onClose={(e) => {
                        e.preventDefault();
                        revokeGrant(g);
                      }}
                      style={{
                        opacity: revokingId === g.id ? 0.5 : 1,
                        pointerEvents: revokingId === g.id ? "none" : undefined,
                      }}
                    >
                      {g.database_name} ({grantLabel(g)})
                    </Tag>
                  ))}
                </Space>
              );
            }}
          />
          <Table.Column<DatabaseUser>
            dataIndex="created_at"
            title={t("databaseuserslist.created")}
            sorter={{ multiple: 2 }}
            render={(date: string) => shortDateTime(date)}
            width={120}
          />
          <Table.Column<DatabaseUser>
            title={t("databaseuserslist.actions")}
            dataIndex="actions"
            width={180}
            render={(_, row) => (
              <RowActions
                actions={[
                  { key: "grant", label: "Grant", icon: <PlusOutlined />, onClick: () => setGrantTarget(row), tooltip: "Add database access" },
                  { key: "rotate", label: "Rotate", icon: <KeyOutlined />, loading: rotatingId === row.id, onClick: () => rotate(row), tooltip: "Rotate password" },
                  {
                    key: "delete",
                    label: "Delete",
                    icon: <DeleteOutlined />,
                    danger: true,
                    onClick: () => { void deleteMutation.mutateAsync({ id: row.id }); },
                    confirm: { title: `Delete user "${row.username}"?`, okText: "Delete" },
                  },
                ]}
              />
            )}
          />
        </SearchableTableStringQ>
      </Card>

      <DatabaseUserPasswordModal
        open={passwordModal !== null}
        username={passwordModal?.username ?? ""}
        password={passwordModal?.password ?? ""}
        title={passwordModal?.title ?? "Database user password"}
        onClose={() => setPasswordModal(null)}
      />

      <AddGrantModal
        open={grantTarget !== null}
        userId={grantTarget?.id ?? null}
        username={grantTarget?.username ?? ""}
        userEngine={grantTarget?.engine ?? "mariadb"}
        excludedDatabaseIds={
          grantTarget?.grants?.map((g) => g.database_id) ?? []
        }
        onClose={() => setGrantTarget(null)}
        onSuccess={refresh}
      />

      <DatabaseUserDrawer
        open={createDrawerOpen}
        onClose={() => setCreateDrawerOpen(false)}
        onCreated={refresh}
      />
    </div>
  );
};
