// AdminSecurityAppArmor — admin Security tab "AppArmor" sub-tab (M40,
// ADR-0086). Read-only profile list + per-profile complain/enforce
// flip behind a confirm modal. Recent denials feed (last 24h, capped
// 50 rows) below the profile table — answers "what did AppArmor
// actually drop?" without dropping to journalctl.
import { useTranslation } from "react-i18next";
import { Alert, Badge, Button, Card, Checkbox, Descriptions, Drawer, Empty, Modal, Select, Space, Table, Tag, Tooltip, Typography } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
import { useState } from "react";

import {
  type AppArmorDenial,
  type AppArmorProfile,
  useAppArmorStatus,
  useSetAppArmorMode,
} from "../../../hooks/useSecurityAppArmor";

// GH #715: what each profile protects, for the detail drawer.
const PROFILE_DESC: Record<string, string> = {
  "jabali-panel": "Panel API daemon (jabali-panel) — the HTTP control plane; talks to MariaDB/agent over unix sockets.",
  "jabali-bulwark": "Bulwark webmail fronting daemon (Node) — the untrusted-input boundary in front of Stalwart.",
  "stalwart-mail": "Stalwart mail server — SMTP/IMAP/JMAP submission + transport.",
  "jabali-fpm-app": "Per-user PHP-FPM tenant workloads (WordPress + other app PHP) via the fpm-exec wrapper.",
};
const MODE_TINT: Record<AppArmorProfile["mode"], "success" | "warning" | "error" | "default"> = {
  enforce: "success",
  complain: "warning",
  missing: "error",
  "kernel-gated": "default", // GH: intentional kernel-gate skip, not a failure
};

export const AdminSecurityAppArmor = () => {
  const { t } = useTranslation();
  const { data, isLoading, refetch } = useAppArmorStatus();
  const setMode = useSetAppArmorMode();
  const [pendingFlip, setPendingFlip] = useState<{ profile: string; nextMode: "enforce" | "complain" } | null>(null);
  const [ackRisk, setAckRisk] = useState(false); // GH #715: confirm gate for enforce-with-would-denies
  const [detail, setDetail] = useState<string | null>(null);
  const [modeFilter, setModeFilter] = useState<string | undefined>(undefined); // GH #715

  if (isLoading) {
    return (
      <Card title={t("adminsecurityapparmor.apparmor")} size="small">
        <Typography.Text type="secondary">Loading…</Typography.Text>
      </Card>
    );
  }

  if (!data?.enabled) {
    return (
      <Card title={t("adminsecurityapparmor.apparmor")} size="small">
        <Alert
          type="warning"
          showIcon
          message={t("adminsecurityapparmor.apparmor_disabled")}
          description={data?.reason || "AppArmor is not active on this host. Reboot may be required if /etc/jabali/.apparmor-grub-pending exists."}
        />
      </Card>
    );
  }

  return (
    <Card
      title={t("adminsecurityapparmor.apparmor")}
      size="small"
      extra={
        <Button size="small" onClick={() => refetch()}>
          Refresh
        </Button>
      }
    >
      <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
        Path-based MAC profiles for jabali daemons + critical system services.
        New profiles ship in <Tag color="warning">complain</Tag> mode for a
        7-day burn-in soak; flip to <Tag color="success">enforce</Tag> per
        profile after the soak (or via{" "}
        <code>jabali apparmor flip-mature</code>).
      </Typography.Paragraph>
      {(data?.violations?.length ?? 0) > 0 ? (
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 16 }}
          message={`${data?.violations?.length} complain-mode would-deny event(s) in the recent window`}
          description={t("adminsecurityapparmor.complain_mode_profiles_are_logging_would_den")}
        />
      ) : null}
      {(data?.profiles ?? []).some((p) => p.mode === "kernel-gated") && (
        <Alert
          type="info"
          showIcon
          style={{ marginBottom: 16 }}
          message={t("adminsecurityapparmor.apparmor_profiles_intentionally_not_loaded_o")}
          description={
            data?.reason ||
            "This kernel lacks AppArmor unix-socket mediation (Debian 13 / Ubuntu 24.04). Jabali profiles are deliberately not loaded here — attaching them would break DNS/DB over unix sockets. This is expected, not a failure or a security regression."
          }
        />
      )}
      <Space wrap style={{ marginBottom: 12 }}>
        <Select
          allowClear
          size="small"
          placeholder={t("adminsecurityapparmor.filter_mode")}
          style={{ width: 160 }}
          value={modeFilter}
          onChange={setModeFilter}
          options={["enforce", "complain", "missing", "kernel-gated"].map((m) => ({ label: m, value: m }))}
        />
      </Space>
      <Table
        rowKey="name"
        dataSource={modeFilter ? data.profiles.filter((p) => p.mode === modeFilter) : data.profiles}
        loading={isLoading}
        tableLayout="fixed"
        scroll={{ x: "max-content" }}
        size="small"
        pagination={false}
        columns={[
          {
            title: "Profile",
            dataIndex: "name",
            render: (v: string) => (
              <Button type="link" style={{ padding: 0 }} onClick={() => setDetail(v)}>
                <code>{v}</code>
              </Button>
            ),
          },
          {
            title: "Mode",
            dataIndex: "mode",
            width: 140,
            render: (mode: AppArmorProfile["mode"]) => (
              <Badge status={MODE_TINT[mode]} text={mode} />
            ),
          },
          {
            title: "Soak readiness",
            width: 200,
            render: (_: unknown, row: AppArmorProfile) => {
              if (row.mode !== "complain") return <Typography.Text type="secondary">—</Typography.Text>;
              const n = (data.violations ?? []).filter((v) => v.profile === row.name).length;
              return n === 0 ? (
                <Tag color="success">ready to enforce (0 would-deny)</Tag>
              ) : (
                <Tag color="warning">{n} would-deny — not ready</Tag>
              );
            },
          },
          {
            title: "Action",
            width: 200,
            render: (_: unknown, row: AppArmorProfile) =>
              row.mode === "missing" ? (
                <Typography.Text type="danger">not loaded</Typography.Text>
              ) : row.mode === "kernel-gated" ? (
                <Typography.Text type="secondary">kernel-gated</Typography.Text>
              ) : (
                <Button
                  size="small"
                  type={row.mode === "complain" ? "primary" : "default"}
                  onClick={() =>
                    setPendingFlip({
                      profile: row.name,
                      nextMode: row.mode === "complain" ? "enforce" : "complain",
                    })
                  }
                >
                  Flip to {row.mode === "complain" ? "enforce" : "complain"}
                </Button>
              ),
          },
        ]}
      />

      <Card
        size="small"
        title={
          <Space>
            <span>Recent denials (last 24h)</span>
            <Typography.Text type="secondary">
              {data.denials.length === 0
                ? "no DENIED audit lines — clean state"
                : `${data.denials.length} entries`}
            </Typography.Text>
          </Space>
        }
        style={{ marginTop: 16 }}
      >
        {data.denials.length === 0 ? (
          <Empty
            image={Empty.PRESENTED_IMAGE_SIMPLE}
            description={
              <Typography.Text type="secondary">
                No <code>apparmor=&quot;DENIED&quot;</code> entries in the kernel
                log over the last 24h. Profiles in <code>complain</code> mode
                still log violations to <code>journalctl -k</code> as
                <code> ALLOWED</code> with the violated rule — flip to
                <code> enforce</code> after a clean soak window.
              </Typography.Text>
            }
          />
        ) : (
          <Table<AppArmorDenial>
            rowKey={(r) => `${r.timestamp}-${r.profile}-${r.path ?? ""}-${r.operation}`}
            dataSource={data.denials}
            tableLayout="fixed"
            scroll={{ x: "max-content" }}
            size="small"
            pagination={{ pageSize: 10, showSizeChanger: false }}
            columns={[
              {
                title: "When",
                dataIndex: "timestamp",
                width: 180,
                render: (v: string) => v ? new Date(v).toLocaleString() : "—",
              },
              {
                title: "Profile",
                dataIndex: "profile",
                width: 160,
                render: (v: string) => <code>{v}</code>,
              },
              {
                title: "Op",
                dataIndex: "operation",
                width: 100,
              },
              {
                title: "Path",
                dataIndex: "path",
                ellipsis: true,
                render: (v: string) =>
                  v ? (
                    <Tooltip title={v}>
                      <code>{v}</code>
                    </Tooltip>
                  ) : (
                    "—"
                  ),
              },
              {
                title: "Mask",
                width: 110,
                render: (_: unknown, r: AppArmorDenial) =>
                  r.denied_mask ? (
                    <Tag color="red">{r.denied_mask}</Tag>
                  ) : r.requested_mask ? (
                    <Tag>{r.requested_mask}</Tag>
                  ) : (
                    "—"
                  ),
              },
            ]}
          />
        )}
      </Card>

      <Modal
        open={pendingFlip !== null}
        afterOpenChange={(o) => { if (o) setAckRisk(false); }}
        title={
          pendingFlip
            ? `Flip ${pendingFlip.profile} → ${pendingFlip.nextMode}`
            : ""
        }
        okText={t("adminsecurityapparmor.flip")}
        okButtonProps={(() => {
          // GH #715: block flip-to-enforce on a profile with unresolved
          // would-deny events unless the operator explicitly acknowledges —
          // enforcing a noisy profile can break the confined daemon.
          const wd = pendingFlip && pendingFlip.nextMode === "enforce"
            ? (data?.violations ?? []).filter((v) => v.profile === pendingFlip.profile).length
            : 0;
          return wd > 0 ? { danger: true, disabled: !ackRisk } : {};
        })()}
        onCancel={() => setPendingFlip(null)}
        onOk={() => {
          if (!pendingFlip) return;
          setMode.mutate(
            { profile: pendingFlip.profile, mode: pendingFlip.nextMode },
            {
              onSuccess: () => {
                feedback.message.success(`${pendingFlip.profile} → ${pendingFlip.nextMode}`);
                setPendingFlip(null);
              },
              onError: () => feedback.message.error("Flip failed — check agent logs"),
            },
          );
        }}
      >
        {pendingFlip?.nextMode === "enforce" ? (
          <Alert
            type="warning"
            showIcon
            message={t("adminsecurityapparmor.enforce_will_start_denying_paths_caps_not_in")}
            description={t("adminsecurityapparmor.if_the_profile_is_missing_a_path_the_daemon")}
            style={{ marginBottom: 12 }}
          />
        ) : null}
        {pendingFlip?.nextMode === "enforce" &&
        (data?.violations ?? []).filter((v) => v.profile === pendingFlip.profile).length > 0 ? (
          <>
            <Alert
              type="error"
              showIcon
              message={`${(data?.violations ?? []).filter((v) => v.profile === pendingFlip.profile).length} unresolved would-deny event(s) on this profile`}
              description={t("adminsecurityapparmor.this_profile_is_not_soak_ready_enforcing_now")}
              style={{ marginBottom: 12 }}
            />
            <Checkbox checked={ackRisk} onChange={(e) => setAckRisk(e.target.checked)}>
              I understand this may break the daemon and want to enforce anyway.
            </Checkbox>
          </>
        ) : (
          <Typography.Paragraph>
            Complain mode logs would-deny events without enforcing.
            Useful for tuning the profile after a daemon update changed
            its file/cap requirements.
          </Typography.Paragraph>
        )}
      </Modal>
      <Drawer
        title={detail ? `Profile: ${detail}` : ""}
        width={640}
        open={!!detail}
        onClose={() => setDetail(null)}
      >
        {detail
          ? (() => {
              const prof = data.profiles.find((p) => p.name === detail);
              const viols = (data.violations ?? []).filter((v) => v.profile === detail);
              const dens = (data.denials ?? []).filter((d) => d.profile === detail);
              const rows = [
                ...viols.map((v) => ({ ...v, kind: "would-deny" })),
                ...dens.map((d) => ({ ...d, kind: "denied" })),
              ];
              return (
                <>
                  <Descriptions column={1} size="small" bordered>
                    <Descriptions.Item label={t("adminsecurityapparmor.protects")}>{PROFILE_DESC[detail] ?? "—"}</Descriptions.Item>
                    <Descriptions.Item label={t("adminsecurityapparmor.mode")}>{prof?.mode ?? "?"}</Descriptions.Item>
                    <Descriptions.Item label={t("adminsecurityapparmor.would_deny_complain")}>{viols.length}</Descriptions.Item>
                    <Descriptions.Item label={t("adminsecurityapparmor.denied_enforce_blocks")}>{dens.length}</Descriptions.Item>
                  </Descriptions>
                  {prof?.mode === "complain" ? (
                    <Alert
                      style={{ marginTop: 12 }}
                      type={viols.length === 0 ? "success" : "warning"}
                      showIcon
                      message={
                        viols.length === 0
                          ? "Ready to enforce — 0 would-deny events in the soak window."
                          : `Not ready — ${viols.length} would-deny event(s); flipping to enforce now would BLOCK them. Resolve first.`
                      }
                    />
                  ) : null}
                  <Typography.Title level={5} style={{ marginTop: 16 }}>
                    Recent events for this profile
                  </Typography.Title>
                  <Table
                    size="small"
                    pagination={false}
                    scroll={{ x: "max-content" }}
                    dataSource={rows}
                    rowKey={(_, i) => String(i)}
                    locale={{ emptyText: "No denials / would-denies logged for this profile" }}
                    columns={[
                      { title: "Kind", dataIndex: "kind", width: 110, render: (v: string) => <Tag color={v === "denied" ? "red" : "orange"}>{v}</Tag> },
                      { title: "Operation", dataIndex: "operation", width: 120 },
                      { title: "Path", dataIndex: "path", ellipsis: true, render: (v: string) => v || "—" },
                      { title: "Comm", dataIndex: "comm", width: 120, render: (v: string) => v || "—" },
                      { title: "Exe", dataIndex: "exe", ellipsis: true, render: (v: string) => v || "—" },
                      { title: "PID", dataIndex: "pid", width: 80, render: (v: string) => v || "—" },
                      { title: "fsuid", dataIndex: "fsuid", width: 70, render: (v: string) => v || "—" },
                    ]}
                  />
                </>
              );
            })()
          : null}
      </Drawer>
    </Card>
  );
};
