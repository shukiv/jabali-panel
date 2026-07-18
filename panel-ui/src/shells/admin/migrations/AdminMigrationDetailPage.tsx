// Admin Migration detail page — per-job stage timeline.
// Backed by GET /admin/migrations/:id which returns
// { job, stages: [{stage_name, state, started_at, ended_at,
//   bytes_processed, last_error}] }.
//
// Polls every 10s when the job is in a non-terminal state so the
// operator sees stages advance live while the cobra CLI runs.
import { type ReactNode, useEffect, useMemo, useState } from "react";
import {
  Alert,
  Button,
  Card,
  Collapse,
  Descriptions,
  Empty,
  Form,
  Input,
  message,
  Modal,
  Radio,
  Space,
  Spin,
  Progress,
  Steps,
  Tag,
  Typography,
} from "antd";
import {
  ClockCircleOutlined,
  DatabaseOutlined,
  FileOutlined,
  GlobalOutlined,
  HddOutlined,
  InboxOutlined,
  KeyOutlined,
  MailOutlined,
  SafetyOutlined,
  UserOutlined,
} from "@icons";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { useMigrationStream } from "../../../hooks/useMigrationStream";
import { useNavigate, useParams } from "react-router";

import { apiClient } from "../../../apiClient";
import { humanBytes as formatBytes } from "../../../utils/bytes";

type MigrationJob = {
  id: string;
  source_kind: string;
  source_host: string;
  source_user: string;
  target_user_id: string | null;
  dest_user?: string | null;
  dest_domain?: string | null;
  state: string;
  started_at: string;
  ended_at: string | null;
  manifest_json: string | null;
  last_error: string | null;
  created_at: string;
  updated_at: string;
};

type MigrationStage = {
  id: string;
  job_id: string;
  stage_name: string;
  state: string;
  started_at: string | null;
  ended_at: string | null;
  bytes_processed: number;
  last_error: string | null;
  created_at: string;
  updated_at: string;
};

type DetailResponse = {
  job: MigrationJob;
  stages: MigrationStage[];
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

const STAGE_ORDER = ["analyze", "fix_perms", "validate", "restore"];

const STAGE_LABEL: Record<string, string> = {
  analyze: "Analyze",
  fix_perms: "Fix permissions",
  validate: "Validate",
  restore: "Restore",
};

function isTerminal(state: string): boolean {
  return state === "done" || state === "failed" || state === "cancelled";
}

export const AdminMigrationDetailPage = () => {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();

  // ADR-0095 decision 4: prefer SSE for live updates. useQuery still
  // runs as the initial fetch + the source of truth for mutation
  // invalidations, but its refetch cadence drops to 60s — SSE drives
  // real-time snapshots and writes through queryClient.setQueryData
  // so every consumer of the query key stays in sync.
  const queryClientForStream = useQueryClient();
  const stream = useMigrationStream(id ?? null);
  useEffect(() => {
    if (stream.data && id) {
      queryClientForStream.setQueryData<DetailResponse>(
        ["admin-migrations", id],
        stream.data,
      );
    }
  }, [stream.data, id, queryClientForStream]);

  const detail = useQuery<DetailResponse>({
    queryKey: ["admin-migrations", id],
    queryFn: async () => {
      const { data } = await apiClient.get<DetailResponse>(
        `/admin/migrations/${id}`,
      );
      return data;
    },
    enabled: !!id,
    // 60s fallback poll — SSE delivers updates in real time at 2s
    // cadence; this is just a safety net if the EventSource drops.
    refetchInterval: (q) => {
      const data = q.state.data as DetailResponse | undefined;
      if (!data) return 5_000;
      return isTerminal(data.job.state) ? false : 60_000;
    },
  });

  // Build the Steps progress prop from the stage rows. Stages
  // table keeps insertion order; when a stage hasn't been created
  // yet we render it as 'wait' (greyed) so the operator sees the
  // full pipeline even before the runner reaches each stage.
  const stagesByName = useMemo(() => {
    const m = new Map<string, MigrationStage>();
    for (const s of detail.data?.stages ?? []) {
      m.set(s.stage_name, s);
    }
    return m;
  }, [detail.data?.stages]);

  useEffect(() => {
    // Soft-refetch when the URL :id changes so navigating directly
    // from the list-page row doesn't show a stale row briefly.
    if (id) detail.refetch();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id]);

  if (!id) {
    return <Empty description="No migration job selected" />;
  }
  if (detail.isLoading || !detail.data) {
    return (
      <div style={{ textAlign: "center", padding: 48 }}>
        <Spin />
      </div>
    );
  }
  const { job, stages } = detail.data;

  return (
    <Space direction="vertical" size="large" style={{ width: "100%" }}>
      <Card>
        <Space wrap style={{ marginBottom: 16, rowGap: 4 }}>
          <Typography.Title level={4} style={{ margin: 0 }}>
            Migration {job.source_user}@{job.source_host}
          </Typography.Title>
          <Tag color={STATE_TAG[job.state]?.color ?? "default"}>
            {STATE_TAG[job.state]?.label ?? job.state}
          </Tag>
          <Typography.Link onClick={() => navigate("/jabali-admin/migrations")}>
            ← back to list
          </Typography.Link>
        </Space>

        <Descriptions size="small" column={{ xs: 1, sm: 2 }} bordered>
          <Descriptions.Item label="Job ID">
            <Typography.Text code>{job.id}</Typography.Text>
          </Descriptions.Item>
          <Descriptions.Item label="Source kind">
            {job.source_kind}
          </Descriptions.Item>
          <Descriptions.Item label="Started">
            {new Date(job.started_at).toLocaleString()}
          </Descriptions.Item>
          <Descriptions.Item label="Ended">
            {job.ended_at ? new Date(job.ended_at).toLocaleString() : "—"}
          </Descriptions.Item>
          <Descriptions.Item label="Target user ID">
            {job.target_user_id ? (
              <Typography.Text code>{job.target_user_id}</Typography.Text>
            ) : (
              "—"
            )}
          </Descriptions.Item>
          <Descriptions.Item label="Destination user">
            {job.dest_user ? job.dest_user : <Typography.Text type="secondary">—</Typography.Text>}
          </Descriptions.Item>
          <Descriptions.Item label="Destination domain">
            {job.dest_domain ? job.dest_domain : <Typography.Text type="secondary">—</Typography.Text>}
          </Descriptions.Item>
          <Descriptions.Item label="Last error">
            {job.last_error ?? "—"}
          </Descriptions.Item>
        </Descriptions>
      </Card>

      <Card size="small" title="Pipeline">
        {(() => {
          const done = STAGE_ORDER.filter((n) => stagesByName.get(n)?.state === "done").length;
          const pct = job.state === "done" ? 100 : Math.round((done / STAGE_ORDER.length) * 100);
          return (
            <Progress
              percent={pct}
              status={job.state === "failed" ? "exception" : job.state === "done" ? "success" : "active"}
              style={{ marginBottom: 16 }}
            />
          );
        })()}
        <Steps
          direction="horizontal"
          responsive
          current={STAGE_ORDER.findIndex(
            (n) => stagesByName.get(n)?.state === "running",
          )}
          items={STAGE_ORDER.map((name) => {
            const row = stagesByName.get(name);
            let status: "wait" | "process" | "finish" | "error" = "wait";
            if (row) {
              if (row.state === "done") status = "finish";
              else if (row.state === "running") status = "process";
              else if (row.state === "failed") status = "error";
            }
            return {
              title: STAGE_LABEL[name] ?? name,
              status,
              description: row
                ? `${row.state}${row.bytes_processed ? ` · ${row.bytes_processed} B` : ""}`
                : "pending",
            };
          })}
        />
      </Card>

      <DriveCard jobId={job.id} jobState={job.state} jobSourceKind={job.source_kind} />

      <Card size="small" title="Stage timeline">
        {stages.length === 0 ? (
          <Empty description="No stage rows yet" />
        ) : (
          <Space direction="vertical" style={{ width: "100%" }}>
            {stages.map((s) => (
              <Card key={s.id} size="small" type="inner">
                <Space direction="vertical" style={{ width: "100%" }}>
                  <Space>
                    <Typography.Text strong>
                      {STAGE_LABEL[s.stage_name] ?? s.stage_name}
                    </Typography.Text>
                    <Tag
                      color={
                        s.state === "done"
                          ? "green"
                          : s.state === "running"
                            ? "blue"
                            : s.state === "failed"
                              ? "red"
                              : "default"
                      }
                    >
                      {s.state.toUpperCase()}
                    </Tag>
                  </Space>
                  <Descriptions size="small" column={{ xs: 1, sm: 2 }}>
                    <Descriptions.Item label="Started">
                      {s.started_at
                        ? new Date(s.started_at).toLocaleString()
                        : "—"}
                    </Descriptions.Item>
                    <Descriptions.Item label="Ended">
                      {s.ended_at ? new Date(s.ended_at).toLocaleString() : "—"}
                    </Descriptions.Item>
                    <Descriptions.Item label="Bytes processed">
                      {s.bytes_processed.toLocaleString()}
                    </Descriptions.Item>
                    <Descriptions.Item label="Error">
                      {s.last_error ?? "—"}
                    </Descriptions.Item>
                  </Descriptions>
                </Space>
              </Card>
            ))}
          </Space>
        )}
      </Card>

      {job.manifest_json && <RestoreSummary manifestJSON={job.manifest_json} />}

      {job.state === "failed" && (
        <FailedCard jobId={job.id} onDestroyed={() => navigate("/jabali-admin/migrations")} />
      )}
    </Space>
  );
};

// RestoreSummary parses the manifest_json (an array of warning/info
// strings emitted by the restore stage's per-area writers) into a
// structured stat-card grid. The raw text sits in a collapsed
// Accordion so the operator can still inspect every line when
// diagnosing partial restores.
function RestoreSummary({ manifestJSON }: { manifestJSON: string }) {
  const parsed = useMemo(() => parseRestoreManifest(manifestJSON), [manifestJSON]);

  const stats: Array<{
    label: string;
    value: number;
    icon: ReactNode;
    iconBg: string;
    iconColor: string;
    fmt?: (n: number) => string;
  }> = [
    {
      label: "Home bytes",
      value: parsed.homeBytes,
      icon: <HddOutlined />,
      iconBg: "#fff7e6",
      iconColor: "#fa8c16",
      fmt: formatBytes,
    },
    {
      label: "Home files",
      value: parsed.homeFiles,
      icon: <FileOutlined />,
      iconBg: "#fff7e6",
      iconColor: "#fa8c16",
    },
    {
      label: "Databases",
      value: parsed.databasesCreated,
      icon: <DatabaseOutlined />,
      iconBg: "#e6f4ff",
      iconColor: "#1677ff",
    },
    {
      label: "DB users",
      value: parsed.dbUsersCreated,
      icon: <UserOutlined />,
      iconBg: "#e6f4ff",
      iconColor: "#1677ff",
    },
    {
      label: "Domains",
      value: parsed.domainsCreated,
      icon: <GlobalOutlined />,
      iconBg: "#f0f5ff",
      iconColor: "#2f54eb",
    },
    {
      label: "Email enabled",
      value: parsed.emailEnabled,
      icon: <MailOutlined />,
      iconBg: "#f9f0ff",
      iconColor: "#722ed1",
    },
    {
      label: "Mailboxes",
      value: parsed.mailboxesCreated,
      icon: <InboxOutlined />,
      iconBg: "#f9f0ff",
      iconColor: "#722ed1",
    },
    {
      label: "Messages pushed",
      value: parsed.messagesPushed,
      icon: <MailOutlined />,
      iconBg: "#f9f0ff",
      iconColor: "#722ed1",
    },
    {
      label: "DNS zones",
      value: parsed.dnsZones,
      icon: <GlobalOutlined />,
      iconBg: "#e6fffb",
      iconColor: "#13c2c2",
    },
    {
      label: "DNS records",
      value: parsed.dnsRecords,
      icon: <GlobalOutlined />,
      iconBg: "#e6fffb",
      iconColor: "#13c2c2",
    },
    {
      label: "SSH keys",
      value: parsed.sshCreated,
      icon: <KeyOutlined />,
      iconBg: "#f6ffed",
      iconColor: "#52c41a",
    },
    {
      label: "Cron jobs",
      value: parsed.cronCreated,
      icon: <ClockCircleOutlined />,
      iconBg: "#f6ffed",
      iconColor: "#52c41a",
    },
    {
      label: "Subdomains",
      value: parsed.subdomainsCreated,
      icon: <GlobalOutlined />,
      iconBg: "#f0f5ff",
      iconColor: "#2f54eb",
    },
    {
      label: "Forwarders",
      value: parsed.forwardersCreated,
      icon: <MailOutlined />,
      iconBg: "#f9f0ff",
      iconColor: "#722ed1",
    },
    {
      label: "Autoresponders",
      value: parsed.autorespondersCreated,
      icon: <MailOutlined />,
      iconBg: "#f9f0ff",
      iconColor: "#722ed1",
    },
    {
      label: "Catch-alls",
      value: parsed.catchallsSet,
      icon: <InboxOutlined />,
      iconBg: "#f9f0ff",
      iconColor: "#722ed1",
    },
    {
      label: "SSL certs",
      value: parsed.sslInstalled,
      icon: <SafetyOutlined />,
      iconBg: "#f6ffed",
      iconColor: "#52c41a",
    },
    {
      label: "FTP observed",
      value: parsed.ftpAccounts,
      icon: <KeyOutlined />,
      iconBg: "#fff1f0",
      iconColor: "#cf1322",
    },
    {
      label: "Inbox filters",
      value: parsed.filtersImported,
      icon: <MailOutlined />,
      iconBg: "#f9f0ff",
      iconColor: "#722ed1",
    },
    {
      label: "PHP pools",
      value: parsed.phpPools,
      icon: <DatabaseOutlined />,
      iconBg: "#fffbe6",
      iconColor: "#d4b106",
    },
    {
      label: "PHP-bound domains",
      value: parsed.phpDomainsBound,
      icon: <GlobalOutlined />,
      iconBg: "#fffbe6",
      iconColor: "#d4b106",
    },
    {
      label: "DKIM keys preserved",
      value: parsed.dkimKeys,
      icon: <SafetyOutlined />,
      iconBg: "#fff7e6",
      iconColor: "#fa8c16",
    },
  ];

  return (
    <Card size="small" title="Restore summary">
      <Space direction="vertical" size="middle" style={{ width: "100%" }}>
        <div
          style={{
            display: "grid",
            gridTemplateColumns: "repeat(auto-fit, minmax(200px, 1fr))",
            gap: 12,
          }}
        >
          {stats.map((s) => (
            <Card key={s.label} size="small" styles={{ body: { padding: 12 } }}>
              <Space size={12} align="center" style={{ width: "100%" }}>
                <div
                  style={{
                    width: 40,
                    height: 40,
                    borderRadius: 10,
                    background: s.iconBg,
                    color: s.iconColor,
                    display: "flex",
                    alignItems: "center",
                    justifyContent: "center",
                    fontSize: 20,
                    flex: "0 0 40px",
                  }}
                >
                  {s.icon}
                </div>
                <Space direction="vertical" size={0} style={{ minWidth: 0 }}>
                  <Typography.Text
                    type="secondary"
                    style={{
                      fontSize: 11,
                      letterSpacing: 0.5,
                      textTransform: "uppercase",
                    }}
                  >
                    {s.label}
                  </Typography.Text>
                  <Typography.Title level={4} style={{ margin: 0, lineHeight: 1.1 }}>
                    {s.fmt ? s.fmt(s.value) : s.value.toLocaleString()}
                  </Typography.Title>
                </Space>
              </Space>
            </Card>
          ))}
        </div>

        {parsed.kratosStatus && (
          <Alert
            type={parsed.kratosStatus === "ok" ? "success" : "warning"}
            showIcon
            icon={<SafetyOutlined />}
            message={`Kratos identity: ${parsed.kratosStatus}`}
            description={
              parsed.kratosNewID ? `new_id=${parsed.kratosNewID}` : undefined
            }
          />
        )}

        {parsed.warnings.length > 0 && (
          <Alert
            type="warning"
            showIcon
            message={`${parsed.warnings.length} warning${parsed.warnings.length === 1 ? "" : "s"} during restore`}
            description={
              <ul style={{ margin: 0, paddingLeft: 18 }}>
                {parsed.warnings.map((w, i) => (
                  <li key={i} style={{ fontSize: 12, wordBreak: "break-word" }}>
                    {w}
                  </li>
                ))}
              </ul>
            }
          />
        )}

        <Collapse
          ghost
          items={[
            {
              key: "raw",
              label: (
                <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                  Raw manifest_json ({parsed.rawCount} entries)
                </Typography.Text>
              ),
              children: (
                <Typography.Paragraph
                  style={{
                    fontFamily: "monospace",
                    fontSize: 12,
                    whiteSpace: "pre-wrap",
                    maxHeight: 320,
                    overflowY: "auto",
                    marginBottom: 0,
                  }}
                >
                  {manifestJSON}
                </Typography.Paragraph>
              ),
            },
          ]}
        />
      </Space>
    </Card>
  );
}

// parseRestoreManifest walks the JSON-encoded array of strings the
// restore stage writes to migration_jobs.manifest_json + extracts
// the per-area counters the operator cares about. Unknown lines
// land in warnings so nothing is silently dropped.
type RestoreParsed = {
  homeBytes: number;
  homeFiles: number;
  databasesCreated: number;
  dbUsersCreated: number;
  domainsCreated: number;
  subdomainsCreated: number;
  emailEnabled: number;
  mailboxesCreated: number;
  messagesPushed: number;
  forwardersCreated: number;
  autorespondersCreated: number;
  filtersImported: number;
  phpPools: number;
  phpDomainsBound: number;
  dkimKeys: number;
  catchallsSet: number;
  sslInstalled: number;
  phpVersion: string;
  ftpAccounts: number;
  dnsZones: number;
  dnsRecords: number;
  sshCreated: number;
  cronCreated: number;
  kratosStatus: string;
  kratosNewID: string;
  warnings: string[];
  rawCount: number;
};

function parseRestoreManifest(raw: string): RestoreParsed {
  const out: RestoreParsed = {
    homeBytes: 0,
    homeFiles: 0,
    databasesCreated: 0,
    dbUsersCreated: 0,
    domainsCreated: 0,
    subdomainsCreated: 0,
    emailEnabled: 0,
    mailboxesCreated: 0,
    messagesPushed: 0,
    forwardersCreated: 0,
    autorespondersCreated: 0,
    filtersImported: 0,
    phpPools: 0,
    phpDomainsBound: 0,
    dkimKeys: 0,
    catchallsSet: 0,
    sslInstalled: 0,
    phpVersion: "",
    ftpAccounts: 0,
    dnsZones: 0,
    dnsRecords: 0,
    sshCreated: 0,
    cronCreated: 0,
    kratosStatus: "",
    kratosNewID: "",
    warnings: [],
    rawCount: 0,
  };

  let lines: string[] = [];
  try {
    const parsed = JSON.parse(raw);
    if (Array.isArray(parsed)) {
      lines = parsed.filter((x): x is string => typeof x === "string");
    }
  } catch {
    // Manifest isn't valid JSON — surface the raw blob as one warning
    // so the collapse panel below still shows the text.
    lines = [raw];
  }
  out.rawCount = lines.length;

  const num = (s: string): number => {
    const n = parseInt(s, 10);
    return Number.isFinite(n) ? n : 0;
  };
  const kv = (line: string, key: string): string => {
    const re = new RegExp(`\\b${key}=([^\\s,"]+)`);
    const m = line.match(re);
    return m ? m[1] : "";
  };

  for (const line of lines) {
    if (line.startsWith("ssh: ")) {
      out.sshCreated += num(kv(line, "created"));
    } else if (line.startsWith("cron: ")) {
      out.cronCreated += num(kv(line, "created"));
    } else if (line.startsWith("databases: ")) {
      out.databasesCreated += num(kv(line, "created"));
    } else if (line.includes("db_user created")) {
      out.dbUsersCreated += 1;
    } else if (line.startsWith("home: ")) {
      out.homeBytes += num(kv(line, "bytes"));
      out.homeFiles += num(kv(line, "files"));
    } else if (line.startsWith("domains: ")) {
      out.domainsCreated += num(kv(line, "created"));
      out.emailEnabled += num(kv(line, "email_enabled"));
    } else if (line.startsWith("mailboxes: ")) {
      out.mailboxesCreated += num(kv(line, "maildirs"));
      out.messagesPushed += num(kv(line, "messages_pushed"));
    } else if (line.includes("mailbox_rows: created")) {
      // mailbox_rows: created <user>@<dom> (temp_pwd=... — change via panel)
      out.mailboxesCreated += 1;
    } else if (line.startsWith("dns: ")) {
      out.dnsZones += num(kv(line, "zones"));
      out.dnsRecords += num(kv(line, "records"));
    } else if (line.startsWith("kratos: ")) {
      out.kratosStatus = kv(line, "status");
      out.kratosNewID = kv(line, "new_id");
    } else if (line.startsWith("ssl: ")) {
      out.sslInstalled += num(kv(line, "installed"));
    } else if (line.startsWith("da_forwarders: ")) {
      // DA migration ships pure-redirect aliases as standalone
      // EmailForwarder rows with MailboxID=NULL (M35 import).
      out.forwardersCreated += num(kv(line, "inserted"));
    } else if (line.startsWith("extras: ")) {
      out.catchallsSet += num(kv(line, "catchalls"));
      out.subdomainsCreated += num(kv(line, "subdomains"));
      out.forwardersCreated += num(kv(line, "forwarders"));
      out.autorespondersCreated += num(kv(line, "autoresponders"));
      out.filtersImported += num(kv(line, "filters"));
      out.phpPools += num(kv(line, "php_pools"));
      out.phpDomainsBound += num(kv(line, "php_domains_bound"));
      out.dkimKeys += num(kv(line, "dkim_keys"));
      out.ftpAccounts += num(kv(line, "ftp_accounts"));
      if (!out.phpVersion) {
        out.phpVersion = kv(line, "php_version");
      }
    } else if (
      line.includes("not found") ||
      line.includes("pending_manual") ||
      line.includes("already imported") ||
      line.includes("cron_disabled_import") ||
      line.includes("env_ignored") ||
      line.includes("php_ini_sanitized") ||
      line.includes("_skipped:") ||
      line.startsWith("warning:") ||
      line.startsWith("skip:")
    ) {
      // Surface actionable migration skips in the warnings card rather
      // than burying them in the raw-manifest collapse. cron_disabled_
      // import (a cron we couldn't safely auto-rewrite — imported
      // disabled with the original command) + env_ignored (MAILTO/SHELL/
      // PATH crontab lines) are the operator's cue to review/enable the
      // cron or re-apply env via the panel.
      out.warnings.push(line);
    }
  }
  return out;
}

function FailedCard({ jobId, onDestroyed }: { jobId: string; onDestroyed: () => void }) {
  // ADR-0095 decision 7 — resume retry is the default; from-scratch
  // is the secondary action for data-drift cases.
  const retry = useMutation({
    mutationFn: async (fromScratch: boolean) => {
      await apiClient.post(
        `/admin/migrations/${jobId}/retry${fromScratch ? "?from_scratch=true" : ""}`,
      );
    },
    onSuccess: () => {
      message.success("Retry queued — runner will pick up the job on the next tick.");
    },
    onError: (e: unknown) => {
      const detail = (e as { response?: { data?: { detail?: string } } })?.response?.data?.detail;
      message.error(detail ?? "Retry failed");
    },
  });
  const destroy = useMutation({
    mutationFn: async () => {
      await apiClient.post(`/admin/migrations/${jobId}/destroy`);
    },
    onSuccess: () => {
      message.success("Job destroyed.");
      onDestroyed();
    },
    onError: (e: unknown) => {
      const detail = (e as { response?: { data?: { detail?: string } } })?.response?.data?.detail;
      message.error(detail ?? "Destroy failed");
    },
  });

  return (
    <Alert
      type="error"
      showIcon
      message="Migration failed"
      description={
        <Space direction="vertical" size="small" style={{ marginTop: 8 }}>
          <Typography.Text>
            Check the stage timeline above for the error detail. Retry
            resumes from the last successful stage; "from scratch" wipes
            stage progress and re-runs from analyze.
          </Typography.Text>
          <Space wrap>
            <Button
              type="primary"
              size="small"
              loading={retry.isPending}
              onClick={() => retry.mutate(false)}
            >
              Retry (resume)
            </Button>
            <Button
              size="small"
              loading={retry.isPending}
              onClick={() => retry.mutate(true)}
            >
              Retry from scratch
            </Button>
            <Button
              danger
              size="small"
              loading={destroy.isPending}
              onClick={() => destroy.mutate()}
            >
              Destroy job
            </Button>
          </Space>
        </Space>
      }
    />
  );
}

/**
 * DriveCard — three actions that drive a migration end-to-end:
 *   1. Upload secrets   → POST /admin/migrations/:id/secrets
 *                         (writes /etc/jabali-panel/migration-secrets/<id>.env)
 *   2. Pull source      → POST /admin/migrations/:id/pull-source
 *                         (transient unit jabali-migrate-pull-<id>.service)
 *   3. Run import       → POST /admin/migrations/:id/import
 *                         (transient unit jabali-migrate-import-<id>.service)
 *
 * Hidden once the job is in a terminal state — the agent endpoints
 * 409 anyway, but UI omitting the buttons is clearer than a flashing
 * error.
 */
function DriveCard({
  jobId,
  jobState,
  jobSourceKind,
}: {
  jobId: string;
  jobState: string;
  jobSourceKind: string;
}) {
  const queryClient = useQueryClient();
  const [secretsOpen, setSecretsOpen] = useState(false);
  const [importOpen, setImportOpen] = useState(false);
  const [credKind, setCredKind] = useState<"password" | "private_key">(
    "password",
  );
  const [secretsForm] = Form.useForm();
  const [importForm] = Form.useForm();

  const refresh = () =>
    queryClient.invalidateQueries({ queryKey: ["admin-migrations", jobId] });

  const uploadSecrets = useMutation({
    mutationFn: async (vals: {
      ssh_password?: string;
      ssh_private_key?: string;
    }) => {
      const { data } = await apiClient.post(
        `/admin/migrations/${jobId}/secrets`,
        vals,
      );
      return data;
    },
    onSuccess: () => {
      message.success("Secrets uploaded.");
      setSecretsOpen(false);
      secretsForm.resetFields();
    },
    onError: (e: unknown) => {
      message.error(
        `Secrets upload failed: ${(e as Error)?.message ?? "unknown"}`,
      );
    },
  });

  const pullSource = useMutation({
    mutationFn: async () => {
      const { data } = await apiClient.post(
        `/admin/migrations/${jobId}/pull-source`,
        { ssh_user: "root" },
      );
      return data as { unit?: string };
    },
    onSuccess: (d) => {
      message.success(`Pull started: ${d?.unit ?? "(unit name unavailable)"}`);
      refresh();
    },
    onError: (e: unknown) => {
      const detail =
        (e as { response?: { data?: { details?: string; error?: string } } })?.response?.data?.details ??
        (e as { response?: { data?: { error?: string } } })?.response?.data?.error ??
        (e as Error)?.message ??
        "unknown";
      message.error(`Pull failed to start: ${detail}`);
    },
  });

  const runImport = useMutation({
    mutationFn: async (vals: {
      target_user: string;
      target_email?: string;
      target_password?: string;
      target_package_id?: string;
    }) => {
      const { data } = await apiClient.post(
        `/admin/migrations/${jobId}/import`,
        vals,
      );
      return data as { unit?: string };
    },
    onSuccess: (d) => {
      message.success(
        `Import started: ${d?.unit ?? "(unit name unavailable)"}`,
      );
      setImportOpen(false);
      importForm.resetFields();
      refresh();
    },
    onError: (e: unknown) => {
      message.error(
        `Import failed to start: ${(e as Error)?.message ?? "unknown"}`,
      );
    },
  });

  // WHM pkgacct (offline) flow swaps the SSH-pull half for a tarball
  // upload. Detected by source_kind so the same DriveCard handles
  // both flows without a parent-side fork.
  const isOffline = jobSourceKind === "whm_pkgacct";

  // Tarball status — drives the "Upload tarball" → "Tarball staged"
  // UI swap. 5s poll while waiting for the upload to finish; once
  // present, polling stops (no new state to wait for).
  const tarballStatus = useQuery<{
    present: boolean;
    size_bytes?: number;
    mtime?: string;
  }>({
    queryKey: ["admin-migrations", jobId, "tarball"],
    enabled: isOffline && !isTerminal(jobState),
    refetchInterval: (q) =>
      q.state.data?.present ? false : 5_000,
    queryFn: async () => {
      const { data } = await apiClient.get(
        `/admin/migrations/${jobId}/tarball`,
      );
      return data;
    },
  });

  const uploadTarball = useMutation({
    mutationFn: async (file: File) => {
      const fd = new FormData();
      fd.append("file", file);
      const { data } = await apiClient.post(
        `/admin/migrations/${jobId}/tarball`,
        fd,
        {
          headers: { "Content-Type": "multipart/form-data" },
          // No timeout — multi-GB uploads need to run for as long as
          // the network sustains them.
          timeout: 0,
        },
      );
      return data as { path: string; size_bytes: number };
    },
    onSuccess: (d) => {
      message.success(
        `Tarball uploaded (${(d.size_bytes / (1024 * 1024)).toFixed(1)} MiB)`,
      );
      queryClient.invalidateQueries({
        queryKey: ["admin-migrations", jobId, "tarball"],
      });
    },
    onError: (e: unknown) => {
      message.error(
        `Tarball upload failed: ${(e as Error)?.message ?? "unknown"}`,
      );
    },
  });

  if (isTerminal(jobState)) {
    return null;
  }

  return (
    <Card size="small" title="Drive migration">
      <Space wrap>
        {isOffline ? (
          <>
            <input
              type="file"
              accept=".tar,.tar.gz,.tgz,.tar.bz2,.tar.xz"
              style={{ display: "none" }}
              id={`tarball-input-${jobId}`}
              onChange={(e) => {
                const f = e.target.files?.[0];
                if (f) uploadTarball.mutate(f);
                e.target.value = "";
              }}
            />
            <Button
              type={tarballStatus.data?.present ? "default" : "primary"}
              loading={uploadTarball.isPending}
              onClick={() =>
                document
                  .getElementById(`tarball-input-${jobId}`)
                  ?.click()
              }
            >
              {tarballStatus.data?.present
                ? "Replace tarball…"
                : "1. Upload tarball…"}
            </Button>
            {tarballStatus.data?.present && (
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                Staged:{" "}
                {((tarballStatus.data.size_bytes ?? 0) / (1024 * 1024)).toFixed(
                  1,
                )}{" "}
                MiB
              </Typography.Text>
            )}
          </>
        ) : (
          <>
            <Button onClick={() => setSecretsOpen(true)}>
              1. Upload secrets…
            </Button>
            <Button
              type="primary"
              loading={pullSource.isPending}
              onClick={() => pullSource.mutate()}
            >
              2. Pull source
            </Button>
          </>
        )}
        <Button
          type="primary"
          loading={runImport.isPending}
          disabled={isOffline && !tarballStatus.data?.present}
          onClick={() => setImportOpen(true)}
        >
          {isOffline ? "2. Run import…" : "3. Run import…"}
        </Button>
      </Space>
      <Typography.Paragraph
        type="secondary"
        style={{ marginTop: 12, marginBottom: 0, fontSize: 12 }}
      >
        {isOffline
          ? "Tarball streams to /var/lib/jabali-migrations/<id>/source.tar.gz on the panel host. Run import once the upload completes — it survives panel restart via a transient systemd unit. Stage rows update on the 10s poll."
          : "Each action runs detached as a transient systemd unit so it survives a panel restart. Stage rows update on the 10s poll."}
      </Typography.Paragraph>

      <Modal
        title="Upload SSH secrets"
        open={secretsOpen}
        onCancel={() => setSecretsOpen(false)}
        onOk={() => secretsForm.submit()}
        confirmLoading={uploadSecrets.isPending}
        okText="Upload"
      >
        <Form
          form={secretsForm}
          layout="vertical"
          onFinish={(vals: { secret: string }) => {
            if (credKind === "password") {
              uploadSecrets.mutate({ ssh_password: vals.secret });
            } else {
              uploadSecrets.mutate({ ssh_private_key: vals.secret });
            }
          }}
        >
          <Form.Item label="Credential type">
            <Radio.Group
              value={credKind}
              onChange={(e) => setCredKind(e.target.value)}
            >
              <Radio.Button value="password">Password</Radio.Button>
              <Radio.Button value="private_key">Private key</Radio.Button>
            </Radio.Group>
          </Form.Item>
          {credKind === "password" ? (
            <Form.Item
              name="secret"
              label="SSH password"
              rules={[{ required: true, message: "required" }]}
            >
              <Input.Password autoComplete="new-password" />
            </Form.Item>
          ) : (
            <Form.Item
              name="secret"
              label="Private key (PEM)"
              rules={[{ required: true, message: "required" }]}
            >
              <Input.TextArea
                rows={8}
                placeholder="-----BEGIN OPENSSH PRIVATE KEY-----"
              />
            </Form.Item>
          )}
        </Form>
      </Modal>

      <Modal
        title="Run import"
        open={importOpen}
        onCancel={() => setImportOpen(false)}
        onOk={() => importForm.submit()}
        confirmLoading={runImport.isPending}
        okText="Run import"
      >
        <Form
          form={importForm}
          layout="vertical"
          onFinish={(vals) => runImport.mutate(vals)}
        >
          <Form.Item
            name="target_user"
            label="Destination username"
            rules={[
              { required: true, message: "required" },
              {
                pattern: /^[a-z][a-z0-9-]{0,31}$/,
                message: "1-32 chars, lowercase, alnum + hyphen",
              },
            ]}
          >
            <Input placeholder="e.g. acme" />
          </Form.Item>
          <Form.Item
            name="target_email"
            label="Email (only if auto-creating)"
          >
            <Input placeholder="owner@example.com" />
          </Form.Item>
          <Form.Item
            name="target_password"
            label="Password (only if auto-creating, ≥10 chars)"
          >
            <Input.Password autoComplete="new-password" />
          </Form.Item>
          <Form.Item
            name="target_package_id"
            label="Package ID (only if auto-creating)"
          >
            <Input placeholder="ULID — leave blank for default package" />
          </Form.Item>
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            Auto-create requires email + password. Pre-existing target
            user matched by username works without these.
          </Typography.Text>
        </Form>
      </Modal>
    </Card>
  );
}

export default AdminMigrationDetailPage;
