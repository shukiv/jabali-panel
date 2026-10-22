// DomainRepairCard — GH #576. Surfaces the operator domain-repair CLIs in the
// admin Repair Center: `jabali domain fix-perms <user>` (idempotent docroot
// group/setgid retrofit) and `jabali domain prune-orphans` (list orphan nginx
// sites; delete is guarded + dry-run-first). Both proxy the CLI via the agent
// (POST /admin/updates/repair/domain/*); nothing is re-implemented client-side.
import { useTranslation } from "react-i18next";
import { useState } from "react";
import {
  Alert,
  App,
  Button,
  Card,
  List,
  Popconfirm,
  Select,
  Space,
  Tag,
  Typography,
} from "antd";
import { FolderOutlined, ToolOutlined, ReloadOutlined } from "@icons";
import { useQuery, useMutation } from "@tanstack/react-query";
import { apiClient } from "../../../apiClient";

const { Text } = Typography;

interface AdminUserLite {
  id: string;
  username?: string | null;
}

interface FixPermsResult {
  output: string;
  exit_code: number;
}

interface OrphanFailure {
  site: string;
  error: string;
}

interface OrphansResult {
  orphans: string[] | null;
  agent_sites?: string[];
  db_domains?: number;
  applied?: boolean;
  // Set by the delete: what went, and what is still there and why (GH #2237).
  deleted?: string[] | null;
  failed?: OrphanFailure[] | null;
}

export function DomainRepairCard() {
  const { t } = useTranslation();
  const { message } = App.useApp();
  const [username, setUsername] = useState<string | undefined>();
  const [fixResult, setFixResult] = useState<FixPermsResult | null>(null);
  const [pruneFailed, setPruneFailed] = useState<OrphanFailure[]>([]);

  // User picker options — only accounts that have a linux username (fix-perms
  // targets docroots under /home/<username>).
  const users = useQuery({
    queryKey: ["admin", "users", "for-fixperms"],
    queryFn: async () => {
      const { data } = await apiClient.get<{ data: AdminUserLite[] }>("/users", {
        params: { page_size: 500 },
      });
      return (data.data ?? []).filter((u) => !!u.username);
    },
  });

  const fixPerms = useMutation({
    mutationFn: async (user: string) => {
      const { data } = await apiClient.post<FixPermsResult>(
        "/admin/updates/repair/domain/fix-perms",
        { username: user },
      );
      return data;
    },
    onSuccess: (data) => {
      setFixResult(data);
      if (data.exit_code === 0) message.success("Permissions repaired");
      else message.warning("Some docroots failed — see output");
    },
    onError: (e: unknown) =>
      message.error(e instanceof Error ? e.message : "fix-perms failed"),
  });

  const orphans = useQuery({
    queryKey: ["admin", "domain-orphans"],
    enabled: false, // manual — only after the admin clicks Scan
    queryFn: async () => {
      const { data } = await apiClient.get<OrphansResult>(
        "/admin/updates/repair/domain/orphans",
      );
      return data;
    },
  });

  const prune = useMutation({
    mutationFn: async () => {
      const { data } = await apiClient.post<OrphansResult>(
        "/admin/updates/repair/domain/orphans/prune",
      );
      return data;
    },
    onMutate: () => setPruneFailed([]),
    onSuccess: (data) => {
      const deleted = data.deleted ?? [];
      const failed = data.failed ?? [];
      setPruneFailed(failed);
      if (failed.length === 0) {
        message.success(
          `Deleted ${deleted.length} orphan site${deleted.length === 1 ? "" : "s"}`,
        );
      } else {
        message.error(
          `${failed.length} orphan site${failed.length === 1 ? " was" : "s were"} not deleted`,
        );
      }
      orphans.refetch();
    },
    onError: (e: unknown) =>
      message.error(e instanceof Error ? e.message : "prune failed"),
  });

  const orphanList = orphans.data?.orphans ?? [];

  return (
    <Card
      title={
        <Space>
          <ToolOutlined />
          Domain repair
        </Space>
      }
    >
      <Space direction="vertical" size={16} style={{ width: "100%" }}>
        <Text type="secondary">
          Operator helpers for domain problems, run on the host via{" "}
          <Text code>jabali domain …</Text>. Safe to re-run.
        </Text>

        {/* ---- fix-perms (safe, idempotent) ---- */}
        <div>
          <Text strong>
            <FolderOutlined /> Repair docroot permissions
          </Text>
          <div>
            <Text type="secondary" style={{ fontSize: 12 }}>
              Retrofits a user's docroots to group www-data + setgid so nginx can
              read files their PHP-FPM pool creates. Use after a migration.
            </Text>
          </div>
          <Space style={{ marginTop: 8 }} wrap>
            <Select
              style={{ minWidth: 220 }}
              placeholder={t("domainrepaircard.select_a_user")}
              loading={users.isLoading}
              showSearch
              optionFilterProp="label"
              value={username}
              onChange={setUsername}
              options={(users.data ?? []).map((u) => ({
                value: u.username as string,
                label: u.username as string,
              }))}
            />
            <Button
              type="primary"
              icon={<ToolOutlined />}
              disabled={!username}
              loading={fixPerms.isPending}
              onClick={() => username && fixPerms.mutate(username)}
            >
              Repair permissions
            </Button>
          </Space>
          {fixResult && (
            <pre
              style={{
                whiteSpace: "pre-wrap",
                marginTop: 8,
                fontSize: 12,
                background: "rgba(0,0,0,0.03)",
                padding: 8,
                borderRadius: 4,
              }}
            >
              {fixResult.output}
            </pre>
          )}
        </div>

        {/* ---- orphan prune (destructive) ---- */}
        <div>
          <Text strong>Orphan nginx sites</Text>
          <div>
            <Text type="secondary" style={{ fontSize: 12 }}>
              Sites present in nginx but with no panel DB row. Scanning is a
              dry-run; deletion is irreversible (removes the vhost, DKIM keys,
              and any Stalwart mail domain row).
            </Text>
          </div>
          <Space style={{ marginTop: 8 }} wrap>
            <Button
              icon={<ReloadOutlined />}
              loading={orphans.isFetching}
              onClick={() => {
                setPruneFailed([]);
                orphans.refetch();
              }}
            >
              Scan for orphans
            </Button>
            <Popconfirm
              title={t("domainrepaircard.delete_all_orphan_sites")}
              description={t("domainrepaircard.this_is_irreversible_it_tears_down_each_site")}
              okText={t("domainrepaircard.delete_permanently")}
              okButtonProps={{ danger: true }}
              cancelText={t("domainrepaircard.cancel")}
              onConfirm={() => prune.mutate()}
              disabled={orphanList.length === 0}
            >
              <Button danger loading={prune.isPending} disabled={orphanList.length === 0}>
                Delete {orphanList.length > 0 ? `${orphanList.length} ` : ""}orphan
                {orphanList.length === 1 ? "" : "s"}
              </Button>
            </Popconfirm>
          </Space>

          {pruneFailed.length > 0 && (
            <Alert
              style={{ marginTop: 8 }}
              type="error"
              showIcon
              message={`${pruneFailed.length} orphan site${pruneFailed.length === 1 ? " was" : "s were"} not deleted`}
              description={
                <List
                  size="small"
                  dataSource={pruneFailed}
                  renderItem={(f) => (
                    <List.Item>
                      <Space direction="vertical" size={0}>
                        <Text code>{f.site}</Text>
                        <Text type="secondary">{f.error}</Text>
                      </Space>
                    </List.Item>
                  )}
                />
              }
            />
          )}
          {orphans.isFetched && orphanList.length === 0 && (
            <Alert
              style={{ marginTop: 8 }}
              type="success"
              showIcon
              message={t("domainrepaircard.no_orphan_sites_found")}
            />
          )}
          {orphanList.length > 0 && (
            <List
              style={{ marginTop: 8 }}
              size="small"
              bordered
              dataSource={orphanList}
              renderItem={(site) => (
                <List.Item>
                  <Tag color="warning">orphan</Tag>
                  <Text code>{site}</Text>
                </List.Item>
              )}
            />
          )}
        </div>
      </Space>
    </Card>
  );
}
