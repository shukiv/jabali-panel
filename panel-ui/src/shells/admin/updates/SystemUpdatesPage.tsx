// SystemUpdatesPage — admin Updates Center (M53, ADR-0118).
//
// Layout mirrors the product mockup: a row of 4 stat cards, then a 2-up grid
// of Jabali Panel + System Packages, then Automatic Updates + Recent History,
// then the Changelog. Data auto-loads on mount (jabali + apt checks fire once)
// so the page shows real numbers immediately instead of empty placeholders.
import { useTranslation } from "react-i18next";
import { useEffect, useState, type CSSProperties } from "react";
import { Alert, Button, Card, Checkbox, Collapse, Col, Empty, Modal, Row, Segmented, Space, Spin, Switch, Table, Tag, TimePicker, Timeline, Tooltip, Typography } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
import dayjs from "dayjs";

import {
  AppstoreOutlined,
  ClockCircleOutlined,
  CodeOutlined,
  DownloadOutlined,
  LoadingOutlined,
  ReloadOutlined,
  SafetyOutlined,
  SettingOutlined,
} from "@icons";

import { JobLogTail } from "../../../components/JobLogTail";
import { apiClient } from "../../../apiClient";

// ReleaseChannelCard — operator picks stable vs development (GH #445). Stable
// tracks promoted, reviewed builds (the movable `stable` tag); Development
// tracks the latest main commit. Persisted in server_settings; the updater +
// behind-count honor it.
const ReleaseChannelCard = () => {
  const { t } = useTranslation();
  const [channel, setChannel] = useState<string>("stable");
  const [saving, setSaving] = useState(false);
  useEffect(() => {
    apiClient
      .get<{ release_channel?: string }>("/admin/settings")
      .then((r) => setChannel(r.data?.release_channel === "development" ? "development" : "stable"))
      .catch(() => {});
  }, []);
  const onChange = async (v: string | number) => {
    const next = String(v);
    const prev = channel;
    setChannel(next);
    setSaving(true);
    try {
      await apiClient.patch("/admin/settings", { release_channel: next });
      feedback.message.success(`Release channel set to ${next}`);
    } catch (e) {
      setChannel(prev);
      feedback.message.error(e instanceof Error ? e.message : "Failed to set release channel");
    } finally {
      setSaving(false);
    }
  };
  return (
    <Card size="small" style={{ marginBottom: 16 }}>
      <Space wrap align="center" size={12}>
        <Typography.Text strong>Release channel</Typography.Text>
        <Segmented
          value={channel}
          disabled={saving}
          onChange={onChange}
          options={[
            { label: "Stable", value: "stable" },
            { label: "Development", value: "development" },
          ]}
        />
        <Tag color={channel === "stable" ? "green" : "orange"}>{channel}</Tag>
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          Stable tracks promoted, reviewed builds; Development follows the latest
          main commit. Keep production on Stable.
        </Typography.Text>
      </Space>
      {channel === "development" && (
        <Alert
          type="warning"
          showIcon
          style={{ marginTop: 12 }}
          message={t("systemupdatespage.development_channel_less_reviewed_builds")}
          description={t("systemupdatespage.development_follows_the_latest_main_commit_a")}
        />
      )}
      <Alert
        type="info"
        showIcon
        style={{ marginTop: 12 }}
        message={t("systemupdatespage.promoting_a_build_to_stable_is_an_operator_c")}
        description={
          <span>
            This switch only chooses which tag this host tracks. Moving the{" "}
            <code>stable</code> tag to a reviewed build is done with{" "}
            <code>jabali release promote</code> on a machine with push access to
            the source remote (a maintainer box or CI) — the panel host is a
            read-only deployment target, so promotion is intentionally CLI-only.
          </span>
        }
      />
    </Card>
  );
};
import { RepairCard } from "./RepairCard";
import { DomainRepairCard } from "./DomainRepairCard";
import {
  useAptCheck,
  useAptRun,
  useAptStatus,
  useAptStop,
  useAutoupdateConfig,
  useJabaliCheck,
  useJabaliRun,
  useJabaliStatus,
  useJabaliStop,
  useUpdateAutoupdate,
  useUpdateHistory,
  useUpdateState,
  type AptCheckError,
  type AptCheckResult,
  type AptPackage,
  type AutoupdateConfig,
  type JabaliCheckResult,
  type UpdateHistoryRow,
} from "../../../hooks/useSystemUpdates";

const { Text, Title } = Typography;

function timeAgo(iso?: string): string {
  if (!iso) return "Never";
  const then = new Date(iso).getTime();
  if (Number.isNaN(then)) return "Never";
  const s = Math.floor((Date.now() - then) / 1000);
  if (s < 60) return "Just now";
  if (s < 3600) return `${Math.floor(s / 60)} min ago`;
  if (s < 86400) return `${Math.floor(s / 3600)} h ago`;
  return `${Math.floor(s / 86400)} d ago`;
}

const sha8 = (s?: string) => (s ? s.substring(0, 8) : "—");

export const SystemUpdatesPage = () => {
  const state = useUpdateState();
  const jabali = useJabaliCheck();
  const apt = useAptCheck();

  // Auto-load once on mount so cards/tables show data without a manual click.
  useEffect(() => {
    jabali.mutate();
    apt.mutate();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const onCheckNow = async () => {
    try {
      await Promise.all([jabali.mutateAsync(), apt.mutateAsync()]);
      await state.refetch();
      feedback.message.success("Checked for updates");
    } catch (e: unknown) {
      feedback.message.error(e instanceof Error ? e.message : "check failed");
    }
  };

  return (
    <div>
      <Title level={3} style={{ marginTop: 0, marginBottom: 16 }}>
        <DownloadOutlined /> Updates
      </Title>
      <UpdateWarningAlert />
      <ReleaseChannelCard />
      <Space direction="vertical" size={16} style={{ width: "100%" }}>
        <StatCards
          state={state.data}
          jabali={jabali.data}
          apt={apt.data}
          checking={jabali.isPending || apt.isPending}
          onCheckNow={onCheckNow}
        />
        <Row gutter={[16, 16]}>
          <Col xs={24} xl={12}>
            <JabaliPanelCard check={jabali} />
          </Col>
          <Col xs={24} xl={12}>
            <SystemPackagesCard check={apt} />
          </Col>
        </Row>
        <Row gutter={[16, 16]}>
          <Col xs={24} xl={12}>
            <RepairCard />
          </Col>
          <Col xs={24} xl={12}>
            <DomainRepairCard />
          </Col>
        </Row>
        <Row gutter={[16, 16]}>
          <Col xs={24} xl={12}>
            <AutomaticUpdatesCard />
          </Col>
          <Col xs={24} xl={12}>
            <RecentHistoryCard />
          </Col>
        </Row>
      </Space>
    </div>
  );
};

// --- stat cards ------------------------------------------------------------

type StatCardsProps = {
  state?: {
    jabali_behind: number;
    jabali_current_sha?: string;
    apt_total: number;
    apt_security: number;
    apt_checked_at?: string;
    jabali_checked_at?: string;
  };
  jabali?: JabaliCheckResult;
  apt?: AptCheckResult;
  checking: boolean;
  onCheckNow: () => void;
};

function StatCards({ state, jabali, apt, checking, onCheckNow }: StatCardsProps) {
  const behind = jabali?.behind_count ?? state?.jabali_behind ?? 0;
  const aptTotal = apt?.total ?? state?.apt_total ?? 0;
  const aptSecurity = apt?.security_total ?? state?.apt_security ?? 0;
  const installed = apt?.installed_total;
  const lastChecked = state?.apt_checked_at ?? state?.jabali_checked_at;

  const cardBody: CSSProperties = { display: "flex", gap: 14, alignItems: "flex-start" };
  // Background is a translucent tint of the icon color so the box reads the
  // same on light and dark themes (hardcoded pastels were invisible/clashing
  // in dark mode). `${color}22` ~= 13% alpha.
  const iconBox = (color: string): CSSProperties => ({
    width: 44,
    height: 44,
    borderRadius: 10,
    background: `${color}22`,
    color,
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    fontSize: 20,
    flex: "none",
  });

  return (
    <Row gutter={[16, 16]}>
      <Col xs={24} sm={12} xl={6}>
        <Card style={{ height: "100%" }}>
          <div style={cardBody}>
            <span style={iconBox("#2563eb")}>
              <CodeOutlined />
            </span>
            <div>
              <Text type="secondary">Panel Version</Text>
              <div style={{ margin: "4px 0" }}>
                <Text strong style={{ fontSize: 20 }}>{sha8(jabali?.current_sha ?? state?.jabali_current_sha)}</Text>{" "}
                {behind === 0 ? (
                  <Tag color="green">Up to date</Tag>
                ) : (
                  <Tag color="orange">{behind} behind</Tag>
                )}
              </div>
              <Text type="secondary" style={{ fontSize: 12 }}>
                Latest: {sha8(jabali?.remote_sha)}
              </Text>
            </div>
          </div>
        </Card>
      </Col>

      <Col xs={24} sm={12} xl={6}>
        <Card style={{ height: "100%" }}>
          <div style={cardBody}>
            <span style={iconBox("#7c3aed")}>
              <AppstoreOutlined />
            </span>
            <div>
              <Text type="secondary">System Packages</Text>
              <div style={{ margin: "4px 0" }}>
                <Text strong style={{ fontSize: 20 }}>{aptTotal}</Text>{" "}
                {aptTotal > 0 ? (
                  <Tag color="orange">Updates available</Tag>
                ) : (
                  <Tag color="green">Up to date</Tag>
                )}
              </div>
              <Text type="secondary" style={{ fontSize: 12 }}>
                {installed ? `of ${installed} packages` : "installed packages"}
              </Text>
            </div>
          </div>
        </Card>
      </Col>

      <Col xs={24} sm={12} xl={6}>
        <Card style={{ height: "100%" }}>
          <div style={cardBody}>
            <span style={iconBox("#dc2626")}>
              <SafetyOutlined />
            </span>
            <div>
              <Text type="secondary">Security Updates</Text>
              <div style={{ margin: "4px 0" }}>
                <Text strong style={{ fontSize: 20, color: aptSecurity > 0 ? "#cf1322" : undefined }}>
                  {aptSecurity}
                </Text>{" "}
                {aptSecurity > 0 ? <Tag color="red">Critical</Tag> : <Tag color="green">Clear</Tag>}
              </div>
              <Text type="secondary" style={{ fontSize: 12 }}>
                {aptSecurity > 0 ? "Important security fixes" : "No security updates"}
              </Text>
            </div>
          </div>
        </Card>
      </Col>

      <Col xs={24} sm={12} xl={6}>
        <Card style={{ height: "100%" }}>
          <div style={cardBody}>
            <span style={iconBox("#16a34a")}>
              <ClockCircleOutlined />
            </span>
            <div style={{ flex: "auto", minWidth: 0 }}>
              <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: 8 }}>
                <Text type="secondary" style={{ whiteSpace: "nowrap" }}>Last Checked</Text>
                <Tooltip title="Check for updates">
                  <Button
                    type="text"
                    size="small"
                    icon={<ReloadOutlined />}
                    loading={checking}
                    onClick={onCheckNow}
                    aria-label="Check for updates"
                  />
                </Tooltip>
              </div>
              <div style={{ margin: "4px 0" }}>
                <Text strong style={{ fontSize: 18 }}>{timeAgo(lastChecked)}</Text>
              </div>
              <Text type="secondary" style={{ fontSize: 12 }}>
                {lastChecked ? dayjs(lastChecked).format("MMM D, YYYY HH:mm") : "—"}
              </Text>
            </div>
          </div>
        </Card>
      </Col>
    </Row>
  );
}

// --- Jabali Panel card -----------------------------------------------------

function JabaliPanelCard({ check }: { check: ReturnType<typeof useJabaliCheck> }) {
  const [since, setSince] = useState<string | null>(null);
  const run = useJabaliRun();
  const stop = useJabaliStop();
  const status = useJabaliStatus(since);

  const result = check.data;
  const behind = result?.behind_count ?? 0;
  const running = status.data?.status === "active" || status.data?.status === "activating";
  const finished =
    since !== null && status.data !== undefined && !running && status.data.exit_code !== undefined;
  const succeeded = finished && status.data?.exit_code === 0;
  const commits = result?.recent_commits ?? [];
  const pending = result?.pending_commits ?? [];

  const onRun = async () => {
    try {
      const r = await run.mutateAsync();
      setSince(r.started_at);
      feedback.message.success("Update started");
    } catch (e: unknown) {
      feedback.message.error(e instanceof Error ? e.message : "run failed");
    }
  };
  const onStop = async () => {
    try {
      await stop.mutateAsync();
      feedback.message.success("Stop signal sent");
    } catch (e: unknown) {
      feedback.message.error(e instanceof Error ? e.message : "stop failed");
    }
  };

  return (
    <Card
      style={{ height: "100%" }}
      title={
        <Space>
          <CodeOutlined />
          <span>Jabali Panel</span>
          {check.isPending ? (
            <Tag>checking…</Tag>
          ) : behind === 0 ? (
            <Tag color="green">Up to date</Tag>
          ) : (
            <Tag color="orange">{behind} behind</Tag>
          )}
        </Space>
      }
    >
      <Row gutter={16}>
        <Col span={12}>
          <Text type="secondary">Current Version</Text>
          <div>
            <Text strong code style={{ fontSize: 16 }}>{sha8(result?.current_sha)}</Text>
          </div>
          {result?.branch ? (
            <Text type="secondary" style={{ fontSize: 12 }}>branch {result.branch}</Text>
          ) : null}
        </Col>
        <Col span={12}>
          <Text type="secondary">Latest Version</Text>
          <div>
            <Text strong code style={{ fontSize: 16 }}>{sha8(result?.remote_sha)}</Text>
          </div>
          <Text type="secondary" style={{ fontSize: 12 }}>
            {behind === 0 ? "No updates available" : `${behind} commit${behind === 1 ? "" : "s"} behind`}
          </Text>
        </Col>
      </Row>

      {running ? (
        <Alert
          style={{ marginTop: 16 }}
          type="info"
          icon={<LoadingOutlined />}
          showIcon
          message="Update in progress"
          description={
            <Button danger size="small" loading={stop.isPending} onClick={onStop}>
              Stop
            </Button>
          }
        />
      ) : finished ? (
        <Alert
          style={{ marginTop: 16 }}
          type={succeeded ? "success" : "error"}
          showIcon
          message={succeeded ? "Update completed" : "Update failed"}
          description={`Exit code ${status.data?.exit_code}.`}
        />
      ) : null}

      {status.data && (running || since) ? (
        <JobLogTail
          status={status.data.status}
          logTail={status.data.log_tail}
          exitCode={status.data.exit_code}
        />
      ) : null}

      <Space wrap style={{ marginTop: 16 }}>
        <Button icon={<ReloadOutlined />} loading={check.isPending} onClick={() => check.mutate()}>
          Check for updates
        </Button>
        <Button
          type="primary"
          icon={<DownloadOutlined />}
          loading={run.isPending}
          disabled={running || behind === 0}
          onClick={onRun}
        >
          Update now
        </Button>
      </Space>

      {(behind > 0 && pending.length > 0) || commits.length > 0 ? (
        <Collapse
          ghost
          size="small"
          style={{ marginTop: 8 }}
          defaultActiveKey={behind > 0 && pending.length > 0 ? ["included"] : []}
          items={[
            ...(behind > 0 && pending.length > 0
              ? [
                  {
                    key: "included",
                    label: `Included in this update (${behind})`,
                    children: (
                      <CommitList commits={pending} truncatedMore={behind - pending.length} />
                    ),
                  },
                ]
              : []),
            ...(commits.length > 0
              ? [
                  {
                    key: "recent",
                    label: "Recent changes",
                    children: <CommitList commits={commits} />,
                  },
                ]
              : []),
          ]}
        />
      ) : null}
    </Card>
  );
}

// CommitList (JAB-172) renders a pending/recent commit list capped at `cap`
// with an in-place Show more/less toggle, so a host that has drifted weeks
// behind does not bury the rest of the Updates page under a wall of commits.
// `truncatedMore` is the SEPARATE server-side truncation count (commits beyond
// pendingCommits() git log -50 cap); it renders only once the full available
// list is shown, so the two never double-count.
function CommitList({
  commits,
  cap = 5,
  truncatedMore = 0,
}: {
  commits: { sha: string; subject: string; date?: string | null }[];
  cap?: number;
  truncatedMore?: number;
}) {
  const [expanded, setExpanded] = useState(false);
  const shown = expanded ? commits : commits.slice(0, cap);
  return (
    <>
      <ul style={{ margin: 0, paddingLeft: 18 }}>
        {shown.map((c) => (
          <li key={c.sha} style={{ marginBottom: 4 }}>
            <Text>{c.subject}</Text>{" "}
            <Text type="secondary" code style={{ fontSize: 11 }}>
              {c.sha}
            </Text>{" "}
            <Text type="secondary" style={{ fontSize: 11 }}>
              {c.date ? dayjs(c.date).format("MMM D") : ""}
            </Text>
          </li>
        ))}
      </ul>
      {commits.length > cap ? (
        <Button
          type="link"
          size="small"
          style={{ padding: 0, height: "auto", fontSize: 11 }}
          onClick={() => setExpanded((v) => !v)}
        >
          {expanded ? "Show less" : `Show more (${commits.length - cap})…`}
        </Button>
      ) : null}
      {truncatedMore > 0 && (expanded || commits.length <= cap) ? (
        <Text type="secondary" style={{ fontSize: 11, display: "block" }}>
          + {truncatedMore} more…
        </Text>
      ) : null}
    </>
  );
}

// aptErrorMeta maps a structured apt-check failure reason (JAB-10) to a title
// and Alert severity. apt_locked is a transient "in progress" state (warning),
// the rest are real errors.
function aptErrorMeta(reason: string): { title: string; type: "warning" | "error" } {
  switch (reason) {
    case "apt_locked":
      return { title: "A package operation is in progress", type: "warning" };
    case "repo_unreachable":
      return { title: "Couldn't reach the package repositories", type: "error" };
    case "permission":
      return { title: "Insufficient privileges to check packages", type: "error" };
    default:
      return { title: "Couldn't check system packages", type: "error" };
  }
}

// SystemPackagesError renders the structured apt-check diagnostic (JAB-10):
// a plain-language reason + recovery hint, the failed command / exit code /
// stderr under a collapse, and a Retry. When a lock is held by a running apt
// upgrade the retry is disabled so we don't stack duplicate checks.
function SystemPackagesError({
  err,
  running,
  retrying,
  onRetry,
}: {
  err: AptCheckError;
  running: boolean;
  retrying: boolean;
  onRetry: () => void;
}) {
  const meta = aptErrorMeta(err.reason);
  const lockedDuringRun = err.reason === "apt_locked" && running;
  return (
    <Alert
      type={meta.type}
      showIcon
      style={{ marginTop: 8 }}
      message={meta.title}
      description={
        <Space direction="vertical" size={8} style={{ width: "100%" }}>
          <Text>{err.hint}</Text>
          <Collapse
            ghost
            size="small"
            items={[
              {
                key: "details",
                label: "Diagnostics",
                children: (
                  <Space direction="vertical" size={2} style={{ width: "100%" }}>
                    <Text type="secondary">
                      Command: <Text code>{err.command}</Text>
                      {err.exit_code >= 0 ? <> · exit {err.exit_code}</> : null}
                    </Text>
                    {err.stderr ? (
                      <pre
                        style={{
                          margin: 0,
                          whiteSpace: "pre-wrap",
                          fontSize: 12,
                          maxHeight: 160,
                          overflow: "auto",
                        }}
                      >
                        {err.stderr}
                      </pre>
                    ) : null}
                  </Space>
                ),
              },
            ]}
          />
          <Button
            size="small"
            icon={<ReloadOutlined />}
            loading={retrying}
            disabled={lockedDuringRun}
            onClick={onRetry}
          >
            {lockedDuringRun ? "Waiting for the update to finish…" : "Retry"}
          </Button>
        </Space>
      }
    />
  );
}

// UpdateWarningAlert — JAB-137. The production-safety warning kept the full
// SSH recovery command block always-visible, making the card tall enough to
// push the actual update controls below the fold. Keep the core cautions (the
// bullets) always shown, and progressively disclose the detailed recovery
// commands behind a "Show recovery steps" toggle (collapsed by default each
// load, so the page stays compact). No safety content is removed.
export function UpdateWarningAlert() {
  const { t } = useTranslation();
  const [showRecovery, setShowRecovery] = useState(false);
  return (
    <Alert
      type="warning"
      showIcon
      style={{ marginBottom: 16 }}
      message={t("systemupdatespage.update_carefully_especially_system_packages")}
      description={
        <div>
          <ul style={{ margin: "4px 0 8px", paddingLeft: 18 }}>
            <li>System package upgrades can restart services, change packages, or briefly break access.</li>
            <li>Make sure you have direct <strong>SSH access as root</strong> (or a sudo admin) first — do not rely only on this web session as your recovery path.</li>
            <li>Take or verify a recent backup/snapshot before a major update.</li>
            <li>Prefer a maintenance window if this server hosts production sites or mail.</li>
          </ul>
          <Typography.Link
            style={{ fontSize: 12 }}
            onClick={() => setShowRecovery((v) => !v)}
          >
            {showRecovery ? "Hide recovery steps" : "Show recovery steps"}
          </Typography.Link>
          {showRecovery ? (
            <div style={{ marginTop: 8 }}>
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                If something breaks, SSH in as root and:
              </Typography.Text>
              <pre style={{ margin: "4px 0 0", fontSize: 12, whiteSpace: "pre-wrap" }}>{`systemctl status jabali-panel jabali-agent nginx mariadb
journalctl -u jabali-panel -u jabali-agent -u nginx -n 200 --no-pager
jabali repair --diagnose
systemctl restart jabali-panel jabali-agent nginx   # only after reading the logs`}</pre>
            </div>
          ) : null}
        </div>
      }
    />
  );
}

// --- System Packages card --------------------------------------------------

function SystemPackagesCard({ check }: { check: ReturnType<typeof useAptCheck> }) {
  const [since, setSince] = useState<string | null>(null);
  const [securityOnly, setSecurityOnly] = useState(false);
  const run = useAptRun();
  const stop = useAptStop();
  const status = useAptStatus(since);

  const result = check.data;
  const running = status.data?.status === "active" || status.data?.status === "activating";
  const finished =
    since !== null && status.data !== undefined && !running && status.data.exit_code !== undefined;

  const onRun = async () => {
    try {
      const r = await run.mutateAsync();
      setSince(r.started_at);
      feedback.message.success("Apt upgrade started");
    } catch (e: unknown) {
      feedback.message.error(e instanceof Error ? e.message : "run failed");
    }
  };
  const onStop = async () => {
    try {
      await stop.mutateAsync();
      feedback.message.success("Stop signal sent");
    } catch (e: unknown) {
      feedback.message.error(e instanceof Error ? e.message : "stop failed");
    }
  };

  const rows = result
    ? securityOnly
      ? result.packages.filter((p) => p.security)
      : result.packages
    : [];

  return (
    <Card
      style={{ height: "100%" }}
      title={
        <Space>
          <AppstoreOutlined />
          <span>System Packages</span>
        </Space>
      }
      extra={
        result ? (
          <Space size={4}>
            {result.total > 0 ? <Tag color="orange">{result.total} updates available</Tag> : null}
            {(result.security_total ?? 0) > 0 ? (
              <Tag color="red">{result.security_total} critical</Tag>
            ) : null}
          </Space>
        ) : null
      }
    >
      {result?.installed_total ? (
        <Text type="secondary">{result.installed_total} packages installed</Text>
      ) : null}

      {check.isPending && !result ? (
        <div style={{ textAlign: "center", padding: 24 }}>
          <Spin />
        </div>
      ) : result?.error ? (
        // JAB-10: the check ran but apt failed — surface the real reason
        // (lock / repo / perms / command) instead of a bare "up to date" or
        // a generic error. When a lock is held by a running apt upgrade we
        // disable the retry so we don't pile on duplicate checks.
        <SystemPackagesError
          err={result.error}
          running={running}
          retrying={check.isPending}
          onRetry={() => check.mutate()}
        />
      ) : result && result.total === 0 ? (
        <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="System is up to date" />
      ) : result ? (
        <Space direction="vertical" size={12} style={{ width: "100%", marginTop: 8 }}>
          {(result.security_total ?? 0) > 0 ? (
            <Checkbox checked={securityOnly} onChange={(e) => setSecurityOnly(e.target.checked)}>
              Security updates only
            </Checkbox>
          ) : null}
          <Table<AptPackage>
            rowKey="name"
            size="small"
            dataSource={rows}
            pagination={rows.length > 8 ? { pageSize: 8, size: "small" } : false}
            scroll={{ x: "max-content" }}
            columns={[
              { title: "Package", dataIndex: "name" },
              { title: "Current Version", dataIndex: "current" },
              { title: "Latest Version", dataIndex: "new" },
              {
                title: "Type",
                dataIndex: "security",
                width: 100,
                render: (sec: boolean) =>
                  sec ? <Tag color="red">Security</Tag> : <Tag color="orange">Update</Tag>,
              },
            ]}
          />
        </Space>
      ) : check.isError ? (
        <Alert
          type="error"
          showIcon
          style={{ marginTop: 8 }}
          message="Couldn't check system packages"
          description={
            <Button
              size="small"
              icon={<ReloadOutlined />}
              loading={check.isPending}
              onClick={() => check.mutate()}
            >
              Retry
            </Button>
          }
        />
      ) : (
        <Empty
          image={Empty.PRESENTED_IMAGE_SIMPLE}
          description="Run a check to list available package updates"
          style={{ marginTop: 8 }}
        />
      )}

      {running ? (
        <Alert
          style={{ marginTop: 12 }}
          type="info"
          icon={<LoadingOutlined />}
          showIcon
          message="Apt upgrade in progress"
          description={
            <Button danger size="small" loading={stop.isPending} onClick={onStop}>
              Stop
            </Button>
          }
        />
      ) : null}
      {status.data && (running || since) ? (
        <JobLogTail
          status={status.data.status}
          logTail={status.data.log_tail}
          exitCode={status.data.exit_code}
        />
      ) : null}

      <Space wrap style={{ marginTop: 12 }}>
        <Button icon={<ReloadOutlined />} loading={check.isPending} onClick={() => check.mutate()}>
          Check for updates
        </Button>
        {result && result.total > 0 ? (
          <Button
            type="primary"
            icon={<DownloadOutlined />}
            loading={run.isPending}
            disabled={running || finished}
            onClick={onRun}
          >
            Apply updates
          </Button>
        ) : null}
      </Space>
    </Card>
  );
}

// --- Automatic Updates -----------------------------------------------------

function AutomaticUpdatesCard() {
  const { t } = useTranslation();
  const { data, isLoading } = useAutoupdateConfig();
  const state = useUpdateState();
  const save = useUpdateAutoupdate();
  const [draft, setDraft] = useState<AutoupdateConfig | null>(null);
  const [ackOpen, setAckOpen] = useState(false);
  const [ackChecked, setAckChecked] = useState(false);
  const cfg = draft ?? data ?? null;
  const dirty = draft !== null && data !== undefined && JSON.stringify(draft) !== JSON.stringify(data);

  const patch = (p: Partial<AutoupdateConfig>) => {
    if (!cfg) return;
    setDraft({ ...cfg, ...p });
  };
  // JAB-353: turning OS security updates OFF is an intentional, recorded
  // decision — never a silent flip. Enabling clears the opt-out; disabling opens
  // a confirm modal and only then records apt_optout_acknowledged.
  const onAptToggle = (v: boolean) => {
    if (v) {
      patch({ apt_enabled: true, apt_optout_acknowledged: false });
    } else {
      setAckChecked(false);
      setAckOpen(true);
    }
  };
  const confirmDisable = () => {
    patch({ apt_enabled: false, apt_optout_acknowledged: true });
    setAckOpen(false);
  };
  const onSave = async () => {
    if (!cfg) return;
    try {
      await save.mutateAsync(cfg);
      setDraft(null);
      feedback.message.success("Automatic updates saved");
    } catch (e: unknown) {
      feedback.message.error(e instanceof Error ? e.message : "save failed");
    }
  };

  const lastApplied = state.data?.apt_last_applied_at;
  const rebootRequired = state.data?.apt_reboot_required;
  const pendingSecurity = state.data?.apt_security ?? 0;

  return (
    <Card
      style={{ height: "100%" }}
      title={
        <Space>
          <SettingOutlined />
          <span>Automatic Updates</span>
        </Space>
      }
    >
      {isLoading || !cfg ? (
        <div style={{ textAlign: "center", padding: 24 }}><Spin /></div>
      ) : (
        <Space direction="vertical" size={16} style={{ width: "100%" }}>
          {/* JAB-353: persistent, high-visibility warning while OS security
              auto-updates are OFF — an operator opt-out is deliberate but must
              stay visible. */}
          {!cfg.apt_enabled && (
            <Alert
              type="error"
              showIcon
              message="OS security auto-updates are OFF"
              description={
                pendingSecurity > 0
                  ? `${pendingSecurity} security update${pendingSecurity === 1 ? "" : "s"} pending. This host will not apply OS security patches automatically.`
                  : "This host will not apply OS security patches automatically, so known vulnerabilities can remain unpatched."
              }
            />
          )}
          {rebootRequired && (
            <Alert type="warning" showIcon message="Reboot required to finish applying updates" />
          )}
          <Row align="middle" gutter={[12, 12]}>
            <Col flex="auto">
              <Text strong>OS security updates</Text>
              <div>
                <Text type="secondary" style={{ fontSize: 12 }}>
                  Apply Debian/Ubuntu security patches automatically via unattended-upgrades. Separate from the Jabali panel self-update below.
                </Text>
              </div>
              <div style={{ marginTop: 4 }}>
                <Text type="secondary" style={{ fontSize: 12 }}>
                  Last applied:{" "}
                  {lastApplied ? dayjs(lastApplied).format("MMM D, YYYY HH:mm") : "never"}
                  {typeof pendingSecurity === "number" && pendingSecurity > 0 && (
                    <> · <Tag color="red" style={{ marginInlineStart: 4 }}>{pendingSecurity} security pending</Tag></>
                  )}
                </Text>
              </div>
            </Col>
            <Col>
              <TimePicker
                format="HH:mm"
                minuteStep={15}
                allowClear={false}
                disabled={!cfg.apt_enabled}
                value={dayjs(cfg.apt_time, "HH:mm")}
                onChange={(d) => patch({ apt_time: d ? d.format("HH:mm") : cfg.apt_time })}
              />
            </Col>
            <Col>
              <Switch checked={cfg.apt_enabled} onChange={onAptToggle} />
            </Col>
          </Row>

          <Modal
            open={ackOpen}
            title="Disable OS security auto-updates?"
            okText="Disable security updates"
            okButtonProps={{ danger: true, disabled: !ackChecked }}
            onOk={confirmDisable}
            onCancel={() => setAckOpen(false)}
          >
            <Space direction="vertical" size={12}>
              <Text>
                Turning this off means this Internet-facing host will <Text strong>not</Text> apply
                Debian/Ubuntu security patches automatically. Known vulnerabilities in the kernel,
                OpenSSH, OpenSSL, and other exposed components can remain exploitable until you patch
                manually. A Jabali panel update does <Text strong>not</Text> apply OS packages.
              </Text>
              <Checkbox checked={ackChecked} onChange={(e) => setAckChecked(e.target.checked)}>
                I understand the risk and intentionally want OS security auto-updates off.
              </Checkbox>
            </Space>
          </Modal>

          <Row align="middle" gutter={[12, 12]}>
            <Col flex="auto">
              <Text strong>
                Jabali panel self-update{" "}
                <Tooltip title={t("systemupdatespage.a_bad_self_update_can_take_the_panel_offline")}>
                  <Tag color="orange">advanced</Tag>
                </Tooltip>
              </Text>
              <div>
                <Text type="secondary" style={{ fontSize: 12 }}>
                  Run <code>jabali update</code> automatically on a schedule.
                </Text>
              </div>
            </Col>
            <Col>
              <TimePicker
                format="HH:mm"
                minuteStep={15}
                allowClear={false}
                disabled={!cfg.jabali_enabled}
                value={dayjs(cfg.jabali_time, "HH:mm")}
                onChange={(d) => patch({ jabali_time: d ? d.format("HH:mm") : cfg.jabali_time })}
              />
            </Col>
            <Col>
              <Switch checked={cfg.jabali_enabled} onChange={(v) => patch({ jabali_enabled: v })} />
            </Col>
          </Row>

          <Button type="primary" onClick={onSave} disabled={!dirty} loading={save.isPending}>
            Save
          </Button>
        </Space>
      )}
    </Card>
  );
}

// --- Recent History --------------------------------------------------------

const historyStatusTag = (status: string) => {
  if (status === "success") return <Tag color="green">success</Tag>;
  if (status === "failed") return <Tag color="red">failed</Tag>;
  return <Tag color="processing">running</Tag>;
};

function RecentHistoryCard() {
  const { t } = useTranslation();
  const { data, isLoading } = useUpdateHistory(8);
  const rows = data?.items ?? [];
  return (
    <Card
      style={{ height: "100%" }}
      title={
        <Space>
          <ClockCircleOutlined />
          <span>Recent Update History</span>
        </Space>
      }
    >
      {isLoading ? (
        <div style={{ textAlign: "center", padding: 24 }}><Spin /></div>
      ) : rows.length === 0 ? (
        <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t("systemupdatespage.no_update_runs_yet")} />
      ) : (
        <Timeline
          items={rows.map((r: UpdateHistoryRow) => ({
            color: r.status === "success" ? "green" : r.status === "failed" ? "red" : "blue",
            children: (
              <Space direction="vertical" size={0}>
                <Space>
                  <Text>{dayjs(r.started_at).format("MMM D, YYYY HH:mm")}</Text>
                  {historyStatusTag(r.status)}
                </Space>
                <Text type="secondary">
                  {r.kind === "jabali" ? "Jabali panel" : "System packages"} {r.action}
                  {r.summary ? ` — ${r.summary}` : ""}
                </Text>
              </Space>
            ),
          }))}
        />
      )}
    </Card>
  );
}
