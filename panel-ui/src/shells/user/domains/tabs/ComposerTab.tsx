// ComposerTab — the Composer pane of the tenant Web Domain page (GH #2276,
// ADR-0176). It reads GET /domains/:id/composer (whether the package allows
// Composer, where the domain's composer.json files are, the run in progress)
// and starts, follows and cancels runs through /domains/:id/composer/runs.
//
// Install and Update are separate on purpose: Install puts in what
// composer.lock lists, Update moves packages to newer versions and rewrites
// the lock, so Update (and Remove) ask first and say to take a backup.
// Cancelling stops Composer but does not undo what it already wrote.
//
// A domain can name its own project folder (PUT /composer/settings): domains/
// or a folder in it, which several of the user's domains may share. The tab
// then looks only there and runs there; one run at a time per folder.
//
// The page shows this tab only when /me/server-capabilities says the package
// includes Composer; the reasons below cover the rest (a suspended account).
import { useEffect, useRef, useState } from "react";
import {
  Alert,
  Button,
  Checkbox,
  Dropdown,
  Empty,
  Form,
  Input,
  Modal,
  Popconfirm,
  Radio,
  Select,
  Skeleton,
  Space,
  Spin,
  Table,
  Tag,
  Typography,
} from "antd";
import {
  CloseCircleOutlined,
  DeleteOutlined,
  DownloadOutlined,
  DownOutlined,
  EditOutlined,
  EyeOutlined,
  PlusOutlined,
  SyncOutlined,
} from "@icons";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "react-router";

import { apiClient } from "../../../../apiClient";
import { CollapsibleAlert } from "../../../../components/CollapsibleAlert";
import { RowActionButton } from "../../../../components/RowActionButton";
import { Timestamp } from "../../../../components/Timestamp";
import type { Domain } from "../../../../components/domains/types";
import { feedback } from "../../../../lib/feedback";

type ComposerDir = { dir: string; has_lock: boolean; has_vendor: boolean };

// What the domain's project folder holds, when it has one.
type ComposerProject = {
  dir: string;
  exists: boolean;
  has_composer_json: boolean;
  has_lock: boolean;
  has_vendor: boolean;
};

type ComposerDetect = {
  dirs: ComposerDir[];
  project?: ComposerProject;
  php_version?: string;
  php_error?: string;
  composer_error?: string;
};

export type ComposerRun = {
  id: string;
  app_dir: string;
  // The folder the run ran in, relative to the home.
  work_dir?: string;
  command: string;
  packages: string[];
  no_dev: boolean;
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

type ComposerState = {
  enabled: boolean;
  reason?: string;
  // The domain's project folder, relative to the home; null: found on its own.
  project_dir: string | null;
  detect?: ComposerDetect;
  active_run: ComposerRun | null;
};

type RunList = { data: ComposerRun[]; total: number; page: number; page_size: number };

// Why the package doesn't allow a run (GET /composer `reason`).
const REASONS: Record<string, string> = {
  no_account: "This account has no Linux user, so it can't run Composer.",
  account_suspended: "This account is suspended, so it can't run Composer.",
  composer_requires_package:
    "Composer runs come with a hosting package, and this account has none. Ask your administrator.",
  composer_not_in_package: "Your hosting package doesn't include Composer runs. Ask your administrator.",
};

// The commands that take --no-dev (the agent's composerCommands).
const NO_DEV = new Set(["install", "update", "audit", "outdated", "show", "dump-autoload"]);

// Checks that only read the project; they never change composer.json.
const CHECKS: { key: string; label: string }[] = [
  { key: "audit", label: "Audit (security advisories)" },
  { key: "outdated", label: "Outdated packages" },
  { key: "show", label: "Show installed packages" },
  { key: "validate", label: "Validate composer.json" },
  { key: "dump-autoload", label: "Rebuild the autoloader" },
  { key: "diagnose", label: "Diagnose" },
];

// Same rules as the API (composerrun.Validate), so a typo shows in the form.
const PACKAGE_RE = /^[a-z0-9]([_.-]?[a-z0-9]+)*\/[a-z0-9](([_.]|-{1,2})?[a-z0-9]+)*$/;
const CONSTRAINT_RE = /^[A-Za-z0-9.*^~<>=!|, @_+-]{1,64}$/;

const STATE_TAG: Record<ComposerRun["state"], { color: string; label: string }> = {
  running: { color: "blue", label: "Running" },
  succeeded: { color: "green", label: "Succeeded" },
  failed: { color: "red", label: "Failed" },
  timed_out: { color: "orange", label: "Timed out" },
  cancelled: { color: "default", label: "Cancelled" },
};

const folderLabel = (domainName: string, dir: string) =>
  dir ? `domains/${domainName}/${dir}/` : `domains/${domainName}/`;

const runFolder = (run: Pick<ComposerRun, "app_dir" | "work_dir">, domainName: string) =>
  run.work_dir ? `${run.work_dir}/` : folderLabel(domainName, run.app_dir);

// Same rules as the API (composerrun.ValidateProjectDir).
const PROJECT_DIR_RE = /^domains(\/[A-Za-z0-9._-]+)*$/;
const projectDirError = (dir: string) => {
  if (!PROJECT_DIR_RE.test(dir)) return "Use domains/ or a folder inside it: letters, digits, '.', '_', '-' and '/'.";
  if (dir.split("/").some((seg) => seg === "." || seg === "..")) return "The folder may not contain . or ..";
  if (dir.length > 255) return "The folder path is too long.";
  return null;
};

const trimFolder = (dir: string) => dir.trim().replace(/\/+$/, "");

const commandLabel = (run: Pick<ComposerRun, "command" | "packages" | "no_dev">) =>
  [run.command, ...run.packages, run.no_dev ? "--no-dev" : ""].filter(Boolean).join(" ");

type StartBody = { command: string; app_dir: string; packages: string[]; no_dev: boolean };
type DialogKind = "update" | "require" | "remove";

export const ComposerTab = ({ domain }: { domain: Domain }) => {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const base = `/domains/${domain.id}/composer`;

  const stateQ = useQuery<ComposerState>({
    queryKey: ["composer", domain.id],
    queryFn: async () => (await apiClient.get<ComposerState>(base)).data,
  });
  const [page, setPage] = useState(1);
  const runsQ = useQuery<RunList>({
    queryKey: ["composer-runs", domain.id, page],
    queryFn: async () =>
      (await apiClient.get<RunList>(`${base}/runs`, { params: { page, page_size: 10 } })).data,
  });

  // The run shown in the run panel: the one in progress, one just started, or
  // one picked from the history.
  const [watchId, setWatchId] = useState<string | null>(null);
  const activeId = stateQ.data?.active_run?.id ?? null;
  useEffect(() => {
    if (activeId) setWatchId(activeId);
  }, [activeId]);

  const runQ = useQuery<ComposerRun>({
    queryKey: ["composer-run", domain.id, watchId],
    queryFn: async () => (await apiClient.get<ComposerRun>(`${base}/runs/${watchId}`)).data,
    enabled: !!watchId,
    refetchInterval: (q) => (q.state.data?.state === "running" ? 2000 : false),
  });
  const run = runQ.data;
  const running = run?.state === "running" || !!stateQ.data?.active_run;

  // When the watched run finishes, the state and the history change too.
  const lastState = useRef<string | undefined>(undefined);
  useEffect(() => {
    if (lastState.current === "running" && run && run.state !== "running") {
      void qc.invalidateQueries({ queryKey: ["composer", domain.id] });
      void qc.invalidateQueries({ queryKey: ["composer-runs", domain.id] });
    }
    lastState.current = run?.state;
  }, [run, qc, domain.id]);

  const projectDir = stateQ.data?.project_dir ?? null;
  const project = stateQ.data?.detect?.project;
  const dirs = stateQ.data?.detect?.dirs ?? [];
  const [pickedDir, setPickedDir] = useState<string | null>(null);
  // The folder runs use: the project folder when the domain has one and it
  // holds a composer.json, else the detected folder picked.
  const dir: ComposerDir | undefined =
    projectDir !== null
      ? project?.exists && project.has_composer_json
        ? { dir: "", has_lock: project.has_lock, has_vendor: project.has_vendor }
        : undefined
      : (dirs.find((d) => d.dir === pickedDir) ?? dirs[0]);
  const [noDev, setNoDev] = useState(false);
  const [dialog, setDialog] = useState<DialogKind | null>(null);
  const [folderOpen, setFolderOpen] = useState(false);

  const saveFolder = useMutation({
    mutationFn: async (project_dir: string | null) =>
      (await apiClient.put<{ project_dir: string | null }>(`${base}/settings`, { project_dir })).data,
    onSuccess: () => {
      setFolderOpen(false);
      setPickedDir(null);
      feedback.message.success("Project folder saved");
      void qc.invalidateQueries({ queryKey: ["composer", domain.id] });
    },
    onError: (err: Error) => feedback.message.error(err.message),
  });

  const start = useMutation({
    mutationFn: async (body: StartBody) => (await apiClient.post<ComposerRun>(`${base}/runs`, body)).data,
    onSuccess: (started) => {
      setDialog(null);
      setWatchId(started.id);
      void qc.invalidateQueries({ queryKey: ["composer", domain.id] });
      void qc.invalidateQueries({ queryKey: ["composer-runs", domain.id] });
    },
    onError: (err: Error) => {
      feedback.message.error(err.message);
      // A run the server refused is stored as failed; show it in the history.
      void qc.invalidateQueries({ queryKey: ["composer-runs", domain.id] });
    },
  });

  const cancel = useMutation({
    mutationFn: async (id: string) => (await apiClient.post<ComposerRun>(`${base}/runs/${id}/cancel`)).data,
    onSuccess: (cancelled) => {
      qc.setQueryData(["composer-run", domain.id, cancelled.id], cancelled);
      void qc.invalidateQueries({ queryKey: ["composer", domain.id] });
      void qc.invalidateQueries({ queryKey: ["composer-runs", domain.id] });
    },
    onError: (err: Error) => {
      feedback.message.error(err.message);
      void qc.invalidateQueries({ queryKey: ["composer-run", domain.id] });
    },
  });

  const startRun = (command: string, packages: string[] = []) =>
    start.mutate({
      command,
      app_dir: projectDir !== null ? "" : (dir?.dir ?? ""),
      packages,
      no_dev: noDev && NO_DEV.has(command),
    });

  if (stateQ.isLoading) return <Skeleton active />;
  if (stateQ.isError || !stateQ.data) {
    return <Alert type="error" showIcon message="Couldn't load Composer for this domain." />;
  }
  const st = stateQ.data;

  if (!st.enabled) {
    return (
      <Alert
        type="info"
        showIcon
        message="Composer is not available"
        description={REASONS[st.reason ?? ""] ?? "Composer runs are not available for this domain."}
      />
    );
  }

  const det = st.detect;
  return (
    <Space direction="vertical" size="large" style={{ width: "100%" }}>
      <Typography.Paragraph type="secondary" style={{ margin: 0 }}>
        Composer runs as you, in your SSH environment, on the PHP version this domain uses
        {det?.php_version ? ` (PHP ${det.php_version})` : ""}. The project&apos;s composer.json scripts
        run too. A run stops after 30 minutes.
      </Typography.Paragraph>

      <CollapsibleAlert
        storageKey="domain.composer.caution"
        defaultOpen
        type="warning"
        showIcon
        message="Back up before Update or Remove"
        description="Update and Remove change the packages your site runs and rewrite composer.json and composer.lock. Take a backup first, so you can go back if the site breaks. Cancelling a run stops Composer but does not undo what it has already changed."
        action={
          <Button size="small" onClick={() => navigate("/jabali-panel/backups")}>
            Backups
          </Button>
        }
      />

      {!det && (
        <Alert
          type="warning"
          showIcon
          message="The server could not look for composer.json just now. Reload the page to try again."
        />
      )}
      {det?.php_error && <Alert type="error" showIcon message={det.php_error} />}
      {det?.composer_error && <Alert type="error" showIcon message={det.composer_error} />}

      <Space wrap align="center">
        <Typography.Text strong>Composer project</Typography.Text>
        {projectDir !== null ? (
          <Typography.Text code>{`${projectDir}/`}</Typography.Text>
        ) : (
          <Typography.Text type="secondary">Found from the document root up</Typography.Text>
        )}
        <Button size="small" icon={<EditOutlined />} disabled={running} onClick={() => setFolderOpen(true)}>
          Change
        </Button>
      </Space>

      {det && projectDir === null && dirs.length === 0 && (
        <Empty
          description={`No composer.json in this domain's document root or the folders above it, up to ${folderLabel(domain.name, "")}. Upload your project with its composer.json first, or set the folder it is in.`}
        />
      )}
      {det && projectDir !== null && project && !project.exists && (
        <Alert
          type="warning"
          showIcon
          message={`The folder ${projectDir}/ doesn't exist, or is a link.`}
          description="Create it, or change the project folder."
        />
      )}
      {det && projectDir !== null && project?.exists && !project.has_composer_json && (
        <Empty description={`No composer.json in ${projectDir}/ yet. Upload your project with its composer.json first.`} />
      )}

      {dir && (
        <Space direction="vertical" size="middle" style={{ width: "100%" }}>
          <Space wrap align="center">
            <Typography.Text strong>Project folder</Typography.Text>
            {projectDir !== null ? (
              <Typography.Text code>{`${projectDir}/`}</Typography.Text>
            ) : dirs.length > 1 ? (
              <Select
                aria-label="Project folder"
                value={dir.dir}
                onChange={setPickedDir}
                style={{ minWidth: 260 }}
                options={dirs.map((d) => ({ value: d.dir, label: folderLabel(domain.name, d.dir) }))}
              />
            ) : (
              <Typography.Text code>{folderLabel(domain.name, dir.dir)}</Typography.Text>
            )}
            <Tag color={dir.has_lock ? "green" : "default"}>
              {dir.has_lock ? "composer.lock" : "no composer.lock"}
            </Tag>
            <Tag color={dir.has_vendor ? "green" : "default"}>{dir.has_vendor ? "vendor/" : "no vendor/"}</Tag>
          </Space>

          <Space wrap>
            <Button
              type="primary"
              icon={<DownloadOutlined />}
              disabled={running}
              loading={start.isPending && start.variables?.command === "install"}
              onClick={() => startRun("install")}
            >
              Install
            </Button>
            <Button icon={<SyncOutlined />} disabled={running} onClick={() => setDialog("update")}>
              Update
            </Button>
            <Button icon={<PlusOutlined />} disabled={running} onClick={() => setDialog("require")}>
              Add package
            </Button>
            <Button icon={<DeleteOutlined />} disabled={running} onClick={() => setDialog("remove")}>
              Remove package
            </Button>
            <Dropdown
              disabled={running}
              menu={{ items: CHECKS, onClick: ({ key }) => startRun(key) }}
              trigger={["click"]}
            >
              <Button icon={<DownOutlined />} iconPlacement="end">
                More
              </Button>
            </Dropdown>
            <Checkbox checked={noDev} onChange={(e) => setNoDev(e.target.checked)}>
              Production only (skip dev packages)
            </Checkbox>
          </Space>

          <Typography.Text type="secondary">
            {dir.has_lock
              ? "Install puts in exactly the versions composer.lock lists. Update moves packages to newer versions that composer.json allows, and rewrites composer.lock."
              : "There is no composer.lock yet: Install picks the newest versions composer.json allows and writes composer.lock."}
          </Typography.Text>
        </Space>
      )}

      {watchId && (
        <RunPanel
          run={run}
          loading={runQ.isLoading}
          domainName={domain.name}
          cancelling={cancel.isPending}
          onCancel={(id) => cancel.mutate(id)}
          onClose={running ? undefined : () => setWatchId(null)}
        />
      )}

      <div>
        <Typography.Title level={5}>Runs</Typography.Title>
        <Table<ComposerRun>
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
          locale={{ emptyText: "No runs yet" }}
        >
          <Table.Column<ComposerRun>
            title="Started"
            dataIndex="started_at"
            render={(v: string) => <Timestamp value={v} />}
          />
          <Table.Column<ComposerRun>
            title="Command"
            render={(_, r) => <Typography.Text code>{commandLabel(r)}</Typography.Text>}
          />
          <Table.Column<ComposerRun> title="Folder" render={(_, r) => runFolder(r, domain.name)} />
          <Table.Column<ComposerRun> title="Result" render={(_, r) => <RunStateTag run={r} />} />
          <Table.Column<ComposerRun>
            title="Actions"
            render={(_, r) => (
              <RowActionButton icon={<EyeOutlined />} onClick={() => setWatchId(r.id)}>
                View log
              </RowActionButton>
            )}
          />
        </Table>
      </div>

      <RunDialog
        kind={dialog}
        submitting={start.isPending}
        onCancel={() => setDialog(null)}
        onSubmit={(command, packages) => startRun(command, packages)}
      />

      {folderOpen && (
        <ProjectFolderDialog
          domainName={domain.name}
          current={projectDir}
          saving={saveFolder.isPending}
          onCancel={() => setFolderOpen(false)}
          onSave={(folder) => saveFolder.mutate(folder)}
        />
      )}
    </Space>
  );
};

function RunStateTag({ run }: { run: Pick<ComposerRun, "state" | "exit_code"> }) {
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
  domainName,
  cancelling,
  onCancel,
  onClose,
}: {
  run?: ComposerRun;
  loading: boolean;
  domainName: string;
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
    <section aria-label="Composer run">
      <Space direction="vertical" size="small" style={{ width: "100%" }}>
        <Space wrap align="center" style={{ width: "100%", justifyContent: "space-between" }}>
          <Space wrap align="center">
            <Typography.Text code>{commandLabel(run)}</Typography.Text>
            <Typography.Text type="secondary">in {runFolder(run, domainName)}</Typography.Text>
            <RunStateTag run={run} />
            {run.state === "running" && <Spin size="small" />}
          </Space>
          <Space>
            {run.state === "running" && (
              <Popconfirm
                title="Cancel this run?"
                description="Composer stops, but files it has already changed (composer.json, composer.lock, vendor/) stay changed."
                okText="Cancel run"
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

        {run.state === "succeeded" && <Alert type="success" showIcon message="The run finished." />}
        {run.state === "failed" && (
          <Alert
            type="error"
            showIcon
            message={run.exit_code !== null ? `The run failed (exit ${run.exit_code}).` : "The run failed."}
            description={run.error || undefined}
          />
        )}
        {run.state === "timed_out" && (
          <Alert type="error" showIcon message="The run took longer than 30 minutes and was stopped." />
        )}
        {run.state === "cancelled" && (
          <Alert
            type="warning"
            showIcon
            message="The run was cancelled."
            description="Composer may already have changed composer.json, composer.lock or vendor/. Run Install to bring vendor/ back in line with composer.lock."
          />
        )}
        {run.php_defense_blocked && (
          <Alert
            type="error"
            showIcon
            message="PHP Defense blocked a function this run called"
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
            {run.state === "running" ? "Waiting for output…" : "This run wrote no output."}
          </Typography.Text>
        )}
        {run.log_truncated && (
          <Typography.Text type="secondary">Showing the end of the log (the last 256 KiB).</Typography.Text>
        )}
      </Space>
    </section>
  );
}

type DialogValues = { packages?: string[]; package?: string; constraint?: string };

const DIALOG: Record<DialogKind, { title: string; ok: string }> = {
  update: { title: "Update packages", ok: "Run update" },
  require: { title: "Add a package", ok: "Add package" },
  remove: { title: "Remove packages", ok: "Remove" },
};

function RunDialog({
  kind,
  submitting,
  onCancel,
  onSubmit,
}: {
  kind: DialogKind | null;
  submitting: boolean;
  onCancel: () => void;
  onSubmit: (command: string, packages: string[]) => void;
}) {
  const [form] = Form.useForm<DialogValues>();
  if (!kind) return null;

  const packagesRule = {
    validator: (_: unknown, value?: string[]) => {
      const bad = (value ?? []).filter((p) => !PACKAGE_RE.test(p));
      return bad.length
        ? Promise.reject(new Error(`Not a package name (vendor/name): ${bad.join(", ")}`))
        : Promise.resolve();
    },
  };

  const finish = (v: DialogValues) => {
    if (kind === "require") {
      const name = (v.package ?? "").trim();
      const constraint = (v.constraint ?? "").trim();
      onSubmit("require", [constraint ? `${name}:${constraint}` : name]);
      return;
    }
    onSubmit(kind, v.packages ?? []);
  };

  return (
    <Modal
      open
      title={DIALOG[kind].title}
      okText={DIALOG[kind].ok}
      okButtonProps={{ danger: kind === "remove", loading: submitting }}
      onOk={() => form.submit()}
      onCancel={onCancel}
      destroyOnHidden
    >
      {kind !== "require" && (
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 16 }}
          message={
            kind === "update"
              ? "Update can move packages to newer versions and rewrites composer.lock. Take a backup first."
              : "Remove takes the packages out of composer.json, composer.lock and vendor/. Take a backup first."
          }
        />
      )}
      <Form<DialogValues> form={form} layout="vertical" onFinish={finish}>
        {kind === "require" ? (
          <>
            <Form.Item
              name="package"
              label="Package"
              rules={[
                { required: true, message: "Enter a package name" },
                { pattern: PACKAGE_RE, message: "A package name looks like vendor/name" },
              ]}
            >
              <Input placeholder="monolog/monolog" autoComplete="off" autoFocus />
            </Form.Item>
            <Form.Item
              name="constraint"
              label="Version (optional)"
              extra="Leave empty for the newest version that fits, or give a constraint like ^3.0."
              rules={[
                { pattern: CONSTRAINT_RE, message: "Letters, digits, spaces and . * ^ ~ < > = ! | , @ _ + - only" },
              ]}
            >
              <Input placeholder="^3.0" autoComplete="off" />
            </Form.Item>
          </>
        ) : (
          <Form.Item
            name="packages"
            label="Packages"
            extra={
              kind === "update"
                ? "Leave empty to update every package, or name the ones to update."
                : "Name the packages to remove."
            }
            rules={[
              ...(kind === "remove" ? [{ required: true, message: "Name at least one package" }] : []),
              packagesRule,
            ]}
          >
            <Select mode="tags" tokenSeparators={[" ", ","]} placeholder="vendor/name" open={false} suffixIcon={null} />
          </Form.Item>
        )}
      </Form>
    </Modal>
  );
}

type FolderValues = { mode: "auto" | "folder"; dir?: string };

function ProjectFolderDialog({
  domainName,
  current,
  saving,
  onCancel,
  onSave,
}: {
  domainName: string;
  current: string | null;
  saving: boolean;
  onCancel: () => void;
  onSave: (dir: string | null) => void;
}) {
  const [form] = Form.useForm<FolderValues>();
  const mode = Form.useWatch("mode", form);
  return (
    <Modal
      open
      title="Composer project folder"
      okText="Save"
      okButtonProps={{ loading: saving }}
      onOk={() => form.submit()}
      onCancel={onCancel}
      destroyOnHidden
    >
      <Form<FolderValues>
        form={form}
        layout="vertical"
        initialValues={{ mode: current === null ? "auto" : "folder", dir: current ?? "domains" }}
        onFinish={(v) => onSave(v.mode === "auto" ? null : trimFolder(v.dir ?? ""))}
      >
        <Form.Item name="mode">
          <Radio.Group>
            <Space orientation="vertical">
              <Radio value="auto">Find composer.json from the document root up to {folderLabel(domainName, "")}</Radio>
              <Radio value="folder">Use this folder</Radio>
            </Space>
          </Radio.Group>
        </Form.Item>
        {mode === "folder" && (
          <Form.Item
            name="dir"
            label="Folder, from your home directory"
            extra="domains/ or a folder inside it. Several of your domains can use the same folder, to share one composer.json and vendor/. A run uses the PHP version of the domain you start it from, and only one run at a time works in a folder."
            rules={[
              { required: true, message: "Enter a folder" },
              {
                validator: (_: unknown, value?: string) => {
                  const err = projectDirError(trimFolder(value ?? ""));
                  return err ? Promise.reject(new Error(err)) : Promise.resolve();
                },
              },
            ]}
          >
            <Input addonBefore="~/" placeholder="domains" autoComplete="off" />
          </Form.Item>
        )}
      </Form>
    </Modal>
  );
}
