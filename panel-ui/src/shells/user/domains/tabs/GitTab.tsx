// GitTab — the Git pane of the tenant Web Domain page (GH #2277, ADR-0178).
// It reads GET /domains/:id/git (whether the package allows Git deploy, the
// saved repository, the run in progress), saves the repository with PUT,
// starts and follows deployments through /git/deployments, rolls back through
// /git/releases/:release/activate, and manages the deploy-on-push webhook
// (GitWebhookSection).
//
// Saving reads the repository's branches first and stores nothing if that
// fails, so a save can take a while. The token is never sent back: the form
// keeps it unless a new one is typed or "public repository" is ticked.
//
// A deployment builds a new release and only then makes it live, so the site
// keeps serving the old release while one runs, and after a failure. The first
// deployment moves the domain's document root to domains/<domain>/current.
//
// The page shows this tab only when /me/server-capabilities says the package
// includes Git deploy; the reasons below cover the rest (a suspended account).
import { useEffect, useRef, useState } from "react";
import {
  Alert,
  Button,
  Descriptions,
  Drawer,
  Grid,
  Popconfirm,
  Skeleton,
  Space,
  Spin,
  Table,
  Tag,
  Typography,
} from "antd";
import { CloseCircleOutlined, DeleteOutlined, EditOutlined, EyeOutlined, RotateCcwOutlined, UploadOutlined } from "@icons";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { apiClient } from "../../../../apiClient";
import { CollapsibleAlert } from "../../../../components/CollapsibleAlert";
import { RowActionButton } from "../../../../components/RowActionButton";
import { Timestamp } from "../../../../components/Timestamp";
import type { Domain } from "../../../../components/domains/types";
import { feedback } from "../../../../lib/feedback";
import { GitSettingsForm } from "./GitSettingsForm";
import { GitWebhookSection } from "./GitWebhookSection";

export type GitRemote = { default_branch: string; branches: string[]; truncated?: boolean };

export type GitDeployment = {
  id: string;
  kind: "deploy" | "rollback";
  source: "manual" | "webhook";
  // The release the run makes live: its own ID for a deployment, an earlier
  // deployment's for a rollback.
  release_id: string;
  branch: string;
  commit_sha: string;
  doc_root: string;
  php_version: string;
  state: "running" | "succeeded" | "failed" | "timed_out" | "cancelled";
  exit_code: number | null;
  php_defense_blocked: boolean;
  log?: string;
  log_truncated: boolean;
  error: string;
  started_at: string;
  finished_at: string | null;
};

export type GitState = {
  enabled: boolean;
  reason?: string;
  configured: boolean;
  repo_url?: string;
  branch?: string;
  username?: string;
  has_token: boolean;
  verified_at?: string;
  web_dir: string;
  shared_paths: string[];
  deploy_commands: string;
  remote?: GitRemote;
  active_deployment: GitDeployment | null;
};

type GitRelease = { id: string; live: boolean; deployment: GitDeployment | null };
type ReleaseList = { releases: GitRelease[]; live: string };
type DeploymentList = { data: GitDeployment[]; total: number; page: number; page_size: number };

// Why the package doesn't allow Git deploy (GET /git `reason`).
const REASONS: Record<string, string> = {
  no_account: "This account has no Linux user, so it can't deploy from Git.",
  account_suspended: "This account is suspended, so it can't deploy from Git.",
  git_deploy_requires_package:
    "Git deployment comes with a hosting package, and this account has none. Ask your administrator.",
  git_deploy_not_in_package: "Your hosting package doesn't include Git deployment. Ask your administrator.",
};

const STATE_TAG: Record<GitDeployment["state"], { color: string; label: string }> = {
  running: { color: "blue", label: "Running" },
  succeeded: { color: "green", label: "Succeeded" },
  failed: { color: "red", label: "Failed" },
  timed_out: { color: "orange", label: "Timed out" },
  cancelled: { color: "default", label: "Cancelled" },
};

const shortSHA = (sha: string) => sha.slice(0, 7);

const runLabel = (run: Pick<GitDeployment, "kind" | "branch">) =>
  run.kind === "rollback" ? "Rollback" : `Deploy ${run.branch}`;

const commitLabel = (run: Pick<GitDeployment, "branch" | "commit_sha">) =>
  run.commit_sha ? `${run.branch} @ ${shortSHA(run.commit_sha)}` : run.branch;

export const GitTab = ({ domain }: { domain: Domain }) => {
  const qc = useQueryClient();
  const base = `/domains/${domain.id}/git`;
  const screens = Grid.useBreakpoint();
  const isDesktop = screens.lg !== false;

  const stateQ = useQuery<GitState>({
    queryKey: ["git", domain.id],
    queryFn: async () => (await apiClient.get<GitState>(base)).data,
    // A push can start a deployment while the tab is open.
    refetchInterval: (q) => (q.state.data?.configured ? 15_000 : false),
  });
  const configured = !!stateQ.data?.enabled && !!stateQ.data?.configured;

  const [page, setPage] = useState(1);
  const runsQ = useQuery<DeploymentList>({
    queryKey: ["git-deployments", domain.id, page],
    queryFn: async () =>
      (await apiClient.get<DeploymentList>(`${base}/deployments`, { params: { page, page_size: 10 } })).data,
    enabled: configured,
  });
  const releasesQ = useQuery<ReleaseList>({
    queryKey: ["git-releases", domain.id],
    queryFn: async () => (await apiClient.get<ReleaseList>(`${base}/releases`)).data,
    enabled: configured,
  });

  // The run shown in the run panel: the one in progress, one just started, or
  // one picked from the history.
  const [watchId, setWatchId] = useState<string | null>(null);
  const activeId = stateQ.data?.active_deployment?.id ?? null;
  useEffect(() => {
    if (activeId) setWatchId(activeId);
  }, [activeId]);

  const runQ = useQuery<GitDeployment>({
    queryKey: ["git-deployment", domain.id, watchId],
    queryFn: async () => (await apiClient.get<GitDeployment>(`${base}/deployments/${watchId}`)).data,
    enabled: !!watchId,
    refetchInterval: (q) => (q.state.data?.state === "running" ? 2000 : false),
  });
  const run = runQ.data;
  const running = run?.state === "running" || !!stateQ.data?.active_deployment;

  // When the watched run finishes, everything it changed is read again: the
  // history, the releases, the webhook's waiting push, and the domain, whose
  // document root the first deployment moves.
  const lastState = useRef<string | undefined>(undefined);
  useEffect(() => {
    if (lastState.current === "running" && run && run.state !== "running") {
      for (const key of ["git", "git-deployments", "git-releases", "git-webhook"]) {
        void qc.invalidateQueries({ queryKey: [key, domain.id] });
      }
      void qc.invalidateQueries({ queryKey: ["one", "domains", domain.id] });
    }
    lastState.current = run?.state;
  }, [run, qc, domain.id]);

  const refreshAll = () => {
    for (const key of ["git", "git-deployments", "git-releases", "git-webhook"]) {
      void qc.invalidateQueries({ queryKey: [key, domain.id] });
    }
  };

  const [editing, setEditing] = useState(false);

  const onStarted = (started: GitDeployment) => {
    setWatchId(started.id);
    refreshAll();
  };
  const onStartError = (err: Error) => {
    feedback.message.error(err.message);
    // A run the server refused is stored as failed; show it in the history.
    void qc.invalidateQueries({ queryKey: ["git-deployments", domain.id] });
    // git_deployment_active: a push started a run since the last poll.
    void qc.invalidateQueries({ queryKey: ["git", domain.id] });
  };

  const deploy = useMutation({
    mutationFn: async () => (await apiClient.post<GitDeployment>(`${base}/deployments`)).data,
    onSuccess: onStarted,
    onError: onStartError,
  });

  const activate = useMutation({
    mutationFn: async (release: string) =>
      (await apiClient.post<GitDeployment>(`${base}/releases/${release}/activate`)).data,
    onSuccess: onStarted,
    onError: onStartError,
  });

  const cancel = useMutation({
    mutationFn: async (id: string) =>
      (await apiClient.post<GitDeployment>(`${base}/deployments/${id}/cancel`)).data,
    onSuccess: (cancelled) => {
      qc.setQueryData(["git-deployment", domain.id, cancelled.id], cancelled);
      refreshAll();
    },
    onError: (err: Error) => {
      feedback.message.error(err.message);
      void qc.invalidateQueries({ queryKey: ["git-deployment", domain.id] });
    },
  });

  const disconnect = useMutation({
    mutationFn: async () => {
      await apiClient.delete(base);
    },
    onSuccess: () => {
      feedback.message.success("Repository disconnected");
      setWatchId(null);
      refreshAll();
    },
    onError: (err: Error) => feedback.message.error(err.message),
  });

  if (stateQ.isLoading) return <Skeleton active />;
  if (stateQ.isError || !stateQ.data) {
    return <Alert type="error" showIcon message="Couldn't load Git deployment for this domain." />;
  }
  const st = stateQ.data;

  if (!st.enabled) {
    return (
      <Alert
        type="info"
        showIcon
        message="Git deployment is not available"
        description={REASONS[st.reason ?? ""] ?? "Git deployment is not available for this domain."}
      />
    );
  }

  const intro = (
    <Typography.Paragraph type="secondary" style={{ margin: 0 }}>
      A deployment fetches the branch into a new release in domains/{domain.name}/releases/, runs your deploy
      commands there as you, in your SSH environment, and only then makes it live. The site keeps serving the
      current release until then, and after a deployment that fails. A deployment stops after 30 minutes.
    </Typography.Paragraph>
  );

  if (!st.configured) {
    return (
      <Space direction="vertical" size="large" style={{ width: "100%" }}>
        {intro}
        <div style={{ maxWidth: 720 }}>
          <Typography.Title level={5}>Connect a repository</Typography.Title>
          <GitSettingsForm
            domain={domain}
            state={st}
            submitLabel="Connect"
            onSaved={(saved) => {
              qc.setQueryData(["git", domain.id], { ...saved, active_deployment: null });
              refreshAll();
              feedback.message.success(`Connected. Deployments use the ${saved.branch} branch.`);
            }}
          />
        </div>
      </Space>
    );
  }

  const releases = releasesQ.data?.releases ?? [];
  const settings = [
    {
      key: "repo",
      label: "Repository",
      children: <Typography.Text style={{ wordBreak: "break-all" }}>{st.repo_url}</Typography.Text>,
    },
    { key: "branch", label: "Branch", children: <Tag>{st.branch}</Tag> },
    {
      key: "access",
      label: "Access",
      children: st.has_token
        ? `Access token saved${st.username ? ` for ${st.username}` : ""}`
        : "Public repository (no token)",
    },
    {
      key: "web",
      label: "Web directory",
      children: st.web_dir ? (
        <Typography.Text code>{st.web_dir}</Typography.Text>
      ) : (
        <Typography.Text type="secondary">The repository&apos;s root</Typography.Text>
      ),
    },
    {
      key: "shared",
      label: "Shared paths",
      children: st.shared_paths.length ? (
        <Space size={[4, 4]} wrap>
          {st.shared_paths.map((p) => (
            <Tag key={p}>{p}</Tag>
          ))}
        </Space>
      ) : (
        <Typography.Text type="secondary">None</Typography.Text>
      ),
    },
    {
      key: "commands",
      label: "Deploy commands",
      children: st.deploy_commands ? (
        <Typography.Text style={{ whiteSpace: "pre-wrap", fontFamily: "ui-monospace, SFMono-Regular, monospace" }}>
          {st.deploy_commands}
        </Typography.Text>
      ) : (
        <Typography.Text type="secondary">None</Typography.Text>
      ),
    },
    ...(st.verified_at
      ? [{ key: "verified", label: "Last checked", children: <Timestamp value={st.verified_at} /> }]
      : []),
  ];

  return (
    <Space direction="vertical" size="large" style={{ width: "100%" }}>
      {intro}

      <CollapsibleAlert
        storageKey="domain.git.releases"
        defaultOpen
        type="warning"
        showIcon
        message="Each deployment starts from a fresh copy"
        description={`Anything the site writes inside a release, such as uploads or caches, is gone after the next deployment unless it is a shared path. After the first deployment the domain's document root becomes domains/${domain.name}/current${st.web_dir ? `/${st.web_dir}` : ""}.`}
      />

      <div>
        <Typography.Title level={5}>Repository</Typography.Title>
        <Space direction="vertical" size="middle" style={{ width: "100%" }}>
          <Descriptions bordered size="small" column={1} items={settings} />
          <Space wrap>
            <Button
              type="primary"
              icon={<UploadOutlined />}
              disabled={running}
              loading={deploy.isPending}
              onClick={() => deploy.mutate()}
            >
              Deploy now
            </Button>
            <Button icon={<EditOutlined />} onClick={() => setEditing(true)}>
              Edit settings
            </Button>
            <Popconfirm
              title="Disconnect the repository?"
              description={`The repository settings, token and webhook are removed. The site keeps serving its current release; the releases and shared files stay in domains/${domain.name}/.`}
              okText="Disconnect"
              okButtonProps={{ danger: true }}
              onConfirm={() => disconnect.mutate()}
              disabled={running}
            >
              <Button danger icon={<DeleteOutlined />} disabled={running} loading={disconnect.isPending}>
                Disconnect
              </Button>
            </Popconfirm>
          </Space>
        </Space>
      </div>

      {watchId && (
        <RunPanel
          run={run}
          loading={runQ.isLoading}
          cancelling={cancel.isPending}
          onCancel={(id) => cancel.mutate(id)}
          onClose={running ? undefined : () => setWatchId(null)}
        />
      )}

      <GitWebhookSection domainId={domain.id} branch={st.branch ?? ""} />

      <div>
        <Typography.Title level={5}>Deployments</Typography.Title>
        <Table<GitDeployment>
          rowKey="id"
          size="small"
          loading={runsQ.isLoading}
          dataSource={runsQ.data?.data ?? []}
          scroll={{ x: "max-content" }}
          pagination={{
            current: page,
            pageSize: 10,
            total: runsQ.data?.total ?? 0,
            onChange: setPage,
            hideOnSinglePage: true,
          }}
          locale={{ emptyText: "No deployments yet" }}
        >
          <Table.Column<GitDeployment>
            title="Started"
            dataIndex="started_at"
            render={(v: string) => <Timestamp value={v} />}
          />
          <Table.Column<GitDeployment> title="Run" render={(_, r) => runLabel(r)} />
          <Table.Column<GitDeployment>
            title="Commit"
            render={(_, r) => <Typography.Text code>{commitLabel(r)}</Typography.Text>}
          />
          <Table.Column<GitDeployment>
            title="Started by"
            render={(_, r) => (r.source === "webhook" ? "Push" : "Manual")}
          />
          <Table.Column<GitDeployment> title="Result" render={(_, r) => <RunStateTag run={r} />} />
          <Table.Column<GitDeployment>
            title="Actions"
            render={(_, r) => (
              <RowActionButton icon={<EyeOutlined />} onClick={() => setWatchId(r.id)}>
                View log
              </RowActionButton>
            )}
          />
        </Table>
      </div>

      <div>
        <Typography.Title level={5}>Releases</Typography.Title>
        <Typography.Paragraph type="secondary">
          The five newest releases are kept. Making an earlier one live switches the site back to it at once;
          deploy commands don&apos;t run again, so database changes stay as they are.
        </Typography.Paragraph>
        {releasesQ.isError ? (
          <Alert type="error" showIcon message="Couldn't read the releases on the server." />
        ) : (
          <Table<GitRelease>
            rowKey="id"
            size="small"
            loading={releasesQ.isLoading}
            dataSource={releases}
            scroll={{ x: "max-content" }}
            pagination={false}
            locale={{ emptyText: "No releases yet" }}
          >
            <Table.Column<GitRelease>
              title="Deployed"
              render={(_, r) =>
                r.deployment ? (
                  <Timestamp value={r.deployment.started_at} />
                ) : (
                  <Typography.Text code>{r.id}</Typography.Text>
                )
              }
            />
            <Table.Column<GitRelease>
              title="Commit"
              render={(_, r) =>
                r.deployment ? (
                  <Typography.Text code>{commitLabel(r.deployment)}</Typography.Text>
                ) : (
                  <Typography.Text type="secondary">Unknown</Typography.Text>
                )
              }
            />
            <Table.Column<GitRelease> title="Status" render={(_, r) => (r.live ? <Tag color="green">Live</Tag> : null)} />
            <Table.Column<GitRelease>
              title="Actions"
              render={(_, r) =>
                // Only a release a deployment completed can be made live: the
                // server refuses a release a run left half-built.
                !r.live && r.deployment?.kind === "deploy" && r.deployment.state === "succeeded" ? (
                  <Popconfirm
                    title="Make this release live?"
                    description="The site switches to this release now. Deploy commands don't run again."
                    okText="Make live"
                    onConfirm={() => activate.mutate(r.id)}
                    disabled={running}
                  >
                    <RowActionButton icon={<RotateCcwOutlined />} disabled={running}>
                      Make live
                    </RowActionButton>
                  </Popconfirm>
                ) : null
              }
            />
          </Table>
        )}
      </div>

      <Drawer
        title="Git settings"
        open={editing}
        onClose={() => setEditing(false)}
        size={isDesktop ? 560 : "100%"}
        placement="right"
        destroyOnHidden
      >
        <GitSettingsForm
          domain={domain}
          state={st}
          submitLabel="Save"
          onSaved={(saved) => {
            setEditing(false);
            qc.setQueryData(["git", domain.id], { ...saved, active_deployment: st.active_deployment });
            refreshAll();
            feedback.message.success("Git settings saved");
          }}
        />
      </Drawer>
    </Space>
  );
};

function RunStateTag({ run }: { run: Pick<GitDeployment, "state" | "exit_code"> }) {
  const t = STATE_TAG[run.state] ?? { color: "default", label: run.state };
  return (
    <Space size={4}>
      <Tag color={t.color}>{t.label}</Tag>
      {run.exit_code !== null && run.state !== "succeeded" && <Tag>exit {run.exit_code}</Tag>}
    </Space>
  );
}

function RunPanel({
  run,
  loading,
  cancelling,
  onCancel,
  onClose,
}: {
  run?: GitDeployment;
  loading: boolean;
  cancelling: boolean;
  onCancel: (id: string) => void;
  onClose?: () => void;
}) {
  const logRef = useRef<HTMLPreElement | null>(null);
  useEffect(() => {
    if (logRef.current) logRef.current.scrollTop = logRef.current.scrollHeight;
  }, [run?.log]);

  if (loading || !run) return <Skeleton active />;
  return (
    <section aria-label="Git deployment">
      <Space direction="vertical" size="small" style={{ width: "100%" }}>
        <Space wrap align="center" style={{ width: "100%", justifyContent: "space-between" }}>
          <Space wrap align="center">
            <Typography.Text strong>{runLabel(run)}</Typography.Text>
            {run.commit_sha && <Typography.Text code>{shortSHA(run.commit_sha)}</Typography.Text>}
            <RunStateTag run={run} />
            {run.state === "running" && <Spin size="small" />}
          </Space>
          <Space>
            {run.state === "running" && (
              <Popconfirm
                title="Cancel this deployment?"
                description="The new release is removed and the site keeps serving the current one, unless the new release has already gone live."
                okText="Cancel deployment"
                okButtonProps={{ danger: true }}
                cancelText="Keep running"
                onConfirm={() => onCancel(run.id)}
              >
                <Button danger icon={<CloseCircleOutlined />} loading={cancelling}>
                  Cancel
                </Button>
              </Popconfirm>
            )}
            {onClose && <Button onClick={onClose}>Close</Button>}
          </Space>
        </Space>

        {run.state === "succeeded" && (
          <Alert
            type="success"
            showIcon
            message={run.kind === "rollback" ? "The earlier release is live again." : "The new release is live."}
          />
        )}
        {run.state === "failed" && (
          <Alert
            type="error"
            showIcon
            message={
              run.exit_code !== null
                ? `The deployment failed (exit ${run.exit_code}). The site still serves the previous release.`
                : "The deployment failed. The site still serves the previous release."
            }
            description={run.error || undefined}
          />
        )}
        {run.state === "timed_out" && (
          <Alert
            type="error"
            showIcon
            message="The deployment took longer than 30 minutes and was stopped. The site still serves the previous release."
          />
        )}
        {run.state === "cancelled" && (
          <Alert type="warning" showIcon message="The deployment was cancelled. The site still serves the previous release." />
        )}
        {run.php_defense_blocked && (
          <Alert
            type="error"
            showIcon
            message="PHP Defense blocked a function a deploy command called"
            description="The log shows which one. Ask your administrator if the project needs it."
          />
        )}

        {run.log ? (
          <pre
            ref={logRef}
            style={{
              background: "#0a0a0a",
              color: "#d4d4d4",
              padding: 12,
              borderRadius: 6,
              maxHeight: 360,
              overflow: "auto",
              fontFamily: "ui-monospace, SFMono-Regular, monospace",
              fontSize: 12,
              lineHeight: 1.45,
              margin: 0,
              whiteSpace: "pre-wrap",
              wordBreak: "break-word",
            }}
          >
            {run.log}
          </pre>
        ) : (
          <Typography.Text type="secondary">
            {run.state === "running" ? "Waiting for output…" : "This deployment wrote no output."}
          </Typography.Text>
        )}
        {run.log_truncated && (
          <Typography.Text type="secondary">Showing the end of the log (the last 256 KiB).</Typography.Text>
        )}
      </Space>
    </section>
  );
}
