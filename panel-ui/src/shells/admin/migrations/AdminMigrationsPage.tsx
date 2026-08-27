// Admin Migrations page — read-only list view (M35 Step 8 partial).
// Mutation flows (start a new migration, resume, cancel a running
// job) land alongside JMAP-push + DA per-area-builders since those
// gate when the operator can self-service end-to-end. Today the
// page surfaces:
//   - paginated table of migration_jobs
//   - per-row state tag (color-coded) + source kind badge
//   - per-row Cancel button (calls DELETE /admin/migrations/:id)
//   - per-row 'View' link to a future detail page (queued)
//
// Backed by panel-api/internal/api/admin_migrations.go (commit
// 5981541a).
import { useTranslation } from "react-i18next";
import { useEffect, useState } from "react";
import { shortDateTime } from "../../../utils/datetime";
import { Grid, Alert, Button, Card, Empty, List, Popconfirm, Space, Switch, Table, Tag, Tooltip, Typography } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "react-router";

import { apiClient } from "../../../apiClient";
import { RowActions } from "../../../components/RowActions";
import { DeleteOutlined, PlusOutlined, RedoOutlined, SwapOutlined } from "@icons";
import { BulkWhmDrawer } from "./BulkWhmDrawer";
import { RefreshMigrationModal } from "./RefreshMigrationModal";
import { CreateMigrationDrawer } from "./CreateMigrationDrawer";
import { CreateMigrationWizard } from "./CreateMigrationWizard";
import { MigrateImapDrawer } from "./MigrateImapDrawer";

type MigrationJob = {
  id: string;
  batch_id: string | null;
  source_kind: string;
  source_host: string;
  source_user: string;
  target_user_id: string | null;
  state: string;
  started_at: string;
  ended_at: string | null;
  last_error: string | null;
  created_at: string;
  updated_at: string;
};

type MigrationJobListResponse = {
  data: MigrationJob[];
  total: number;
  page: number;
  page_size: number;
};

const STATE_TAG: Record<string, { color: string; label: string }> = {
  pending: { color: "default", label: "PENDING" },
  analyzing: { color: "blue", label: "ANALYZING" },
  fix_perms: { color: "blue", label: "FIX-PERMS" },
  validating: { color: "blue", label: "VALIDATING" },
  restoring: { color: "geekblue", label: "RESTORING" },
  done: { color: "green", label: "DONE" },
  failed: { color: "red", label: "FAILED" },
  cancelled: { color: "default", label: "CANCELLED" },
};

const SOURCE_BADGE: Record<string, { color: string; label: string }> = {
  cpanel: { color: "purple", label: "cPanel" },
  whm_pkgacct: { color: "purple", label: "WHM (pkgacct)" },
  directadmin: { color: "cyan", label: "DirectAdmin" },
  hestiacp: { color: "magenta", label: "HestiaCP" },
};

export const AdminMigrationsPage = () => {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const qc = useQueryClient();
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [bulkOpen, setBulkOpen] = useState(false);
  const [wizardOpen, setWizardOpen] = useState(false);
  const [refreshOpen, setRefreshOpen] = useState(false);
  const [imapOpen, setImapOpen] = useState(false);
  const screens = Grid.useBreakpoint();

  // M35.4 — auto-open wizard on landing if URL carries ?wizard=<id>.
  // Resume-draft buttons + bulk-create redirect both rely on this.
  useEffect(() => {
    if (typeof window === "undefined") return;
    const u = new URL(window.location.href);
    if (u.searchParams.has("wizard")) {
      setWizardOpen(true);
    }
  }, []);

  const list = useQuery<MigrationJobListResponse>({
    queryKey: ["admin-migrations"],
    queryFn: async () => {
      const { data } = await apiClient.get<MigrationJobListResponse>(
        "/admin/migrations?page_size=200",
      );
      return data;
    },
    // 5s only while a job is actually moving; idle lists poll at 60s —
    // slow enough to be cheap, fast enough that a CLI-started migration
    // still shows up without a manual refresh.
    refetchInterval: (q) => {
      const data = q.state.data as MigrationJobListResponse | undefined;
      const live = (data?.data ?? []).some(
        (r) => r.state !== "done" && r.state !== "failed" && r.state !== "cancelled" && r.state !== "draft",
      );
      return live ? 5_000 : 60_000;
    },
  });

  // M35.3 SSRF override toggle. server_settings.migration_allow_private_hosts
  // gates whether the panel will SSH to RFC1918 / loopback targets during
  // discover / pull-source. Default off (production-safe); operator flips
  // for local-network test migrations. ADR-0095 decision 8.
  const settings = useQuery<{ migration_allow_private_hosts?: boolean }>({
    queryKey: ["admin-settings"],
    queryFn: async () => {
      const { data } = await apiClient.get("/admin/settings");
      return data;
    },
  });
  const toggleAllowPrivate = useMutation<void, unknown, { enabled: boolean }>({
    mutationFn: async ({ enabled }) => {
      await apiClient.patch("/admin/settings", {
        migration_allow_private_hosts: enabled,
      });
    },
    onSuccess: async (_d, vars) => {
      feedback.message.success(
        vars.enabled
          ? "Private-host SSRF override ENABLED (restart panel + agent to pick up)"
          : "Private-host SSRF override disabled",
      );
      await qc.invalidateQueries({ queryKey: ["admin-settings"] });
    },
    onError: (err) => {
      const detail =
        (err as { response?: { data?: { error?: string; detail?: string } } })
          ?.response?.data?.detail;
      feedback.message.error(detail ?? "Update failed");
    },
  });

  const cancelBatch = useMutation<void, unknown, { batchId: string }>({
    mutationFn: async ({ batchId }) => {
      await apiClient.delete(`/admin/migrations/batches/${batchId}`);
    },
    onSuccess: async () => {
      feedback.message.success("Batch cancelled");
      await qc.invalidateQueries({ queryKey: ["admin-migrations"] });
    },
    onError: (err) => {
      const detail =
        (err as { response?: { data?: { error?: string; detail?: string } } })
          ?.response?.data?.detail;
      feedback.message.error(detail ?? "Batch cancel failed");
    },
  });

  const cancel = useMutation<void, unknown, { id: string }>({
    mutationFn: async ({ id }) => {
      await apiClient.delete(`/admin/migrations/${id}`);
    },
    onSuccess: async () => {
      feedback.message.success("Cancelled");
      await qc.invalidateQueries({ queryKey: ["admin-migrations"] });
    },
    onError: (err) => {
      const detail =
        (err as { response?: { data?: { error?: string; detail?: string } } })
          ?.response?.data?.detail;
      feedback.message.error(detail ?? "Cancel failed");
    },
  });

  const reKick = useMutation<void, unknown, { id: string }>({
    mutationFn: async ({ id }) => {
      await apiClient.post(`/admin/migrations/${id}/pull-source`);
    },
    onSuccess: async () => {
      feedback.message.success("Pull-source re-dispatched");
      await qc.invalidateQueries({ queryKey: ["admin-migrations"] });
    },
    onError: (err) => {
      const detail =
        (err as { response?: { data?: { error?: string; detail?: string } } })
          ?.response?.data?.detail;
      feedback.message.error(detail ?? "Re-kick failed");
    },
  });

  const destroy = useMutation<void, unknown, { id: string }>({
    mutationFn: async ({ id }) => {
      await apiClient.post(`/admin/migrations/${id}/destroy`);
    },
    onSuccess: async () => {
      feedback.message.success("Destroyed");
      await qc.invalidateQueries({ queryKey: ["admin-migrations"] });
    },
    onError: (err) => {
      const detail =
        (err as { response?: { data?: { error?: string; detail?: string } } })
          ?.response?.data?.detail;
      feedback.message.error(detail ?? "Destroy failed");
    },
  });

  // M35.8: drafts are operator-invisible. Wizard creates them as an
  // internal scratchpad (secret-upload + discover-accounts both need
  // a row to anchor secrets to), but the operator's mental model is
  // "I either start a migration or I don't". Discard/Resume on draft
  // rows confused users — they expected re-typing the connection
  // form, not a Resume-from-state UI. Filter drafts out of the
  // visible list; the daily reaper (24h TTL on drafts) sweeps them.
  const rows = (list.data?.data ?? []).filter((r) => r.state !== "draft");

  const migrationsToolbar = (
          <Space wrap>
            <Tooltip
              title={t("adminmigrationspage.allow_ssh_to_rfc1918_loopback_link_local_sou")}
            >
              <Space size={6}>
                <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                  Allow private IPs
                </Typography.Text>
                <Switch
                  size="small"
                  loading={settings.isLoading || toggleAllowPrivate.isPending}
                  checked={!!settings.data?.migration_allow_private_hosts}
                  onChange={(checked) =>
                    toggleAllowPrivate.mutate({ enabled: checked })
                  }
                />
              </Space>
            </Tooltip>
            <Button onClick={() => setBulkOpen(true)}>Bulk WHM (paste)</Button>
            <Button onClick={() => setWizardOpen(true)}>Wizard</Button>
            <Button onClick={() => setImapOpen(true)}>Migrate IMAP mailbox</Button>
            <Button danger onClick={() => setRefreshOpen(true)}>Refresh (re-pull)</Button>
            <Button
              type="primary"
              icon={<PlusOutlined />}
              onClick={() => setDrawerOpen(true)}
            >
              New migration
            </Button>
          </Space>
  );

  const renderRowActions = (r: MigrationJob) => {
  const terminal =
    r.state === "done" ||
    r.state === "failed" ||
    r.state === "cancelled";
  return (
    <RowActions
      actions={[
        { key: "view", label: "View", icon: <SwapOutlined />, onClick: () => navigate(`/jabali-admin/migrations/${r.id}`) },
        {
          key: "rekick",
          label: "Re-kick",
          icon: <RedoOutlined />,
          hidden: r.state !== "pending",
          onClick: () => reKick.mutate({ id: r.id }),
          confirm: { title: `Re-kick pull-source for ${r.source_user}?`, description: "Re-dispatches the agent's pull-source runner. Use for rows that landed pending before auto-kick existed, or after a transient SSH/network blip.", okText: "Re-kick" },
        },
        {
          key: "cancel",
          label: "Cancel",
          icon: <DeleteOutlined />,
          danger: true,
          hidden: terminal,
          onClick: () => cancel.mutate({ id: r.id }),
          confirm: { title: `Cancel migration ${r.source_user}?`, description: "Stamps the DB row as cancelled. Does NOT kill an in-flight CLI process — Ctrl-C the cobra cmd separately.", okText: "Cancel job" },
        },
        {
          key: "destroy",
          label: "Destroy",
          icon: <DeleteOutlined />,
          danger: true,
          hidden: !terminal,
          onClick: () => destroy.mutate({ id: r.id }),
          confirm: { title: `Destroy migration ${r.source_user}?`, description: "Removes the DB row, secrets file, and /var/lib/jabali-migrations/<id>/ extracted dir. Operator-irreversible.", okText: "Destroy" },
        },
      ]}
    />
  );
  };

  // JAB-34: below the md breakpoint the 8-column Table needs horizontal
  // scroll, which hides State/Started/Ended/actions on a phone. Render each
  // job as a stacked card instead so every field + the row actions are
  // reachable in one tap. Desktop keeps the Table unchanged.
  const renderMobileJobCard = (r: MigrationJob) => {
    const badge = SOURCE_BADGE[r.source_kind] ?? { color: "default", label: r.source_kind };
    const st = STATE_TAG[r.state] ?? { color: "default", label: r.state };
    return (
      <List.Item key={r.id} style={{ paddingInline: 0 }}>
        <Card size="small" style={{ width: "100%" }}>
          <Space direction="vertical" size={6} style={{ width: "100%" }}>
            <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center", gap: 8 }}>
              <Space size={6} wrap>
                <Tag color={badge.color}>{badge.label}</Tag>
                <Tag color={st.color}>{st.label}</Tag>
              </Space>
              {renderRowActions(r)}
            </div>
            <Typography.Text code style={{ fontSize: 12, wordBreak: "break-all" }}>
              {r.source_user}@{r.source_host}
            </Typography.Text>
            <div style={{ display: "flex", justifyContent: "space-between", gap: 8, fontSize: 12 }}>
              <Typography.Text type="secondary">Started {shortDateTime(r.started_at)}</Typography.Text>
              <Typography.Text type="secondary">Ended {r.ended_at ? shortDateTime(r.ended_at) : "\u2014"}</Typography.Text>
            </div>
            {r.batch_id ? (
              <Popconfirm
                title={`Cancel entire batch ${r.batch_id.slice(-6)}?`}
                description={t("adminmigrationspage.cancels_every_non_terminal_job_sharing_this")}
                okText={t("adminmigrationspage.cancel_batch")}
                okButtonProps={{ danger: true }}
                onConfirm={() => cancelBatch.mutate({ batchId: r.batch_id! })}
              >
                <Tag color="purple" style={{ fontFamily: "monospace", fontSize: 11, cursor: "pointer", width: "fit-content" }}>
                  batch {r.batch_id.slice(-6)}
                </Tag>
              </Popconfirm>
            ) : null}
          </Space>
        </Card>
      </List.Item>
    );
  };

  return (
    <Space direction="vertical" size="large" style={{ width: "100%" }}>
      <Alert
        type="info"
        showIcon
        message={t("adminmigrationspage.account_migration_importer_m35")}
        description={
          <Typography.Paragraph style={{ marginBottom: 0 }}>
            Read-only view of the migration_jobs table. Start new migrations
            via the <Typography.Text code>jabali migrate import</Typography.Text>{" "}
            CLI today; admin SPA mutation flows land once the JMAP-push and
            per-area-builder follow-ups stabilise. See{" "}
            <Typography.Text code>plans/m35-migration-importers-runbook.md</Typography.Text>{" "}
            for the per-account workflow.
          </Typography.Paragraph>
        }
      />

      <Card size="small" style={{ marginBottom: 16 }}>
        <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center", flexWrap: "wrap", gap: 12 }}>
          <div>
            <Typography.Title level={4} style={{ margin: 0 }}>
              Start a guided migration
            </Typography.Title>
            <Typography.Text type="secondary">
              One wizard collects connection details, scans the source, lets you review the plan, and runs the migration.
            </Typography.Text>
          </div>
          <Button type="primary" size="large" onClick={() => setWizardOpen(true)}>
            New migration
          </Button>
        </div>
      </Card>

      <Card
        size="small"
        title={t("adminmigrationspage.recent_migration_jobs")}
        extra={screens.md ? migrationsToolbar : undefined}
      >
        {!screens.md ? (
          <div style={{ width: "100%", marginBottom: 12 }}>{migrationsToolbar}</div>
        ) : null}
        {screens.md ? (
        <Table<MigrationJob>
          dataSource={rows}
          rowKey="id"
          loading={list.isLoading}
          pagination={{ defaultPageSize: 50, hideOnSinglePage: true }}
          scroll={{ x: "max-content" }}
          locale={{
            emptyText: (
              <Empty
                image={Empty.PRESENTED_IMAGE_SIMPLE}
                description={t("adminmigrationspage.no_migrations_yet")}
              />
            ),
          }}
        >
          <Table.Column<MigrationJob>
            title={t("adminmigrationspage.source")}
            dataIndex="source_kind"
            sorter={(a, b) => (a.source_kind ?? "").localeCompare(b.source_kind ?? "")}
            render={(k: string) => {
              const b = SOURCE_BADGE[k] ?? { color: "default", label: k };
              return <Tag color={b.color}>{b.label}</Tag>;
            }}
          />
          <Table.Column<MigrationJob>
            title={t("adminmigrationspage.source_host")}
            dataIndex="source_host"
            sorter={(a, b) => (a.source_host ?? "").localeCompare(b.source_host ?? "")}
            render={(s: string) => (
              <Typography.Text code style={{ fontSize: 12 }}>
                {s}
              </Typography.Text>
            )}
          />
          <Table.Column<MigrationJob>
            title={t("adminmigrationspage.batch")}
            dataIndex="batch_id"
            sorter={(a, b) => (a.batch_id ?? "").localeCompare(b.batch_id ?? "")}
            render={(b: string | null) =>
              b ? (
                <Popconfirm
                  title={`Cancel entire batch ${b.slice(-6)}?`}
                  description={t("adminmigrationspage.cancels_every_non_terminal_job_sharing_this")}
                  okText={t("adminmigrationspage.cancel_batch")}
                  okButtonProps={{ danger: true }}
                  onConfirm={() => cancelBatch.mutate({ batchId: b })}
                >
                  <Tag
                    color="purple"
                    style={{ fontFamily: "monospace", fontSize: 11, cursor: "pointer" }}
                  >
                    {b.slice(-6)}
                  </Tag>
                </Popconfirm>
              ) : (
                <Typography.Text type="secondary" style={{ fontSize: 11 }}>
                  —
                </Typography.Text>
              )
            }
          />
          <Table.Column<MigrationJob>
            title={t("adminmigrationspage.source_user")}
            dataIndex="source_user"
            sorter={(a, b) => (a.source_user ?? "").localeCompare(b.source_user ?? "")}
            render={(s: string) => (
              <Typography.Text code style={{ fontSize: 12 }}>
                {s}
              </Typography.Text>
            )}
          />
          <Table.Column<MigrationJob>
            title={t("adminmigrationspage.state")}
            dataIndex="state"
            sorter={(a, b) => (a.state ?? "").localeCompare(b.state ?? "")}
            render={(s: string) => {
              const t = STATE_TAG[s] ?? { color: "default", label: s };
              return <Tag color={t.color}>{t.label}</Tag>;
            }}
          />
          <Table.Column<MigrationJob>
            title={t("adminmigrationspage.started")}
            dataIndex="started_at"
            sorter={(a, b) => (a.started_at ? +new Date(a.started_at) : 0) - (b.started_at ? +new Date(b.started_at) : 0)}
            render={(s: string) => shortDateTime(s)}
          />
          <Table.Column<MigrationJob>
            title={t("adminmigrationspage.ended")}
            dataIndex="ended_at"
            sorter={(a, b) => (a.ended_at ? +new Date(a.ended_at) : 0) - (b.ended_at ? +new Date(b.ended_at) : 0)}
            render={(s: string | null) =>
              s ? shortDateTime(s) : "—"
            }
          />
          <Table.Column<MigrationJob>
            title=""
            width={200}
            render={(_, r) => renderRowActions(r)}
          />
        </Table>
        ) : (
          <List
            dataSource={rows}
            loading={list.isLoading}
            locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t("adminmigrationspage.no_migrations_yet")} /> }}
            renderItem={renderMobileJobCard}
          />
        )}
      </Card>

      <Card size="small" title={t("adminmigrationspage.per_source_kind_support")}>
        <Space direction="vertical" style={{ width: "100%" }}>
          <Typography.Text>
            <Tag color="green">cPanel</Tag> SSH discovery + pkgacct + full
            restore (5 area writers + mailbox stub)
          </Typography.Text>
          <Typography.Text>
            <Tag color="green">WHM (pkgacct)</Tag> Operator-uploaded tarball;
            reuses cPanel restore code-path
          </Typography.Text>
          <Typography.Text>
            <Tag color="green">DirectAdmin</Tag> Live SSH discover +
            system_backup_user tarball pull; restore via cpanel writers
          </Typography.Text>
          <Typography.Text>
            <Tag color="green">HestiaCP</Tag> Live SSH discover +
            v-backup-user tarball pull; restore via cpanel writers
          </Typography.Text>
          <Typography.Text>
            <Tag color="default">IMAP-only</Tag> Not yet wired
          </Typography.Text>
        </Space>
      </Card>
      <CreateMigrationDrawer
        open={drawerOpen}
        onClose={() => setDrawerOpen(false)}
      />
      <RefreshMigrationModal open={refreshOpen} onClose={() => setRefreshOpen(false)} />
      <MigrateImapDrawer open={imapOpen} onClose={() => setImapOpen(false)} />
      <BulkWhmDrawer
        open={bulkOpen}
        onClose={() => setBulkOpen(false)}
        onCreated={() => list.refetch()}
      />
      <CreateMigrationWizard
        open={wizardOpen}
        onClose={() => setWizardOpen(false)}
        onCreated={() => list.refetch()}
      />
    </Space>
  );
};

export default AdminMigrationsPage;
