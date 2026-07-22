// RepairCard — the GUI surface for `jabali repair` on the Updates page.
// "Run Diagnostics" runs the read-only detector sweep and lists what's
// broken; "Auto-Repair safe issues" fires `jabali repair --auto` as a
// transient unit and tails its output until done. Destructive fixes are
// intentionally NOT exposed here — they stay on the CLI (`--all --yes`).
import { useTranslation } from "react-i18next";
import { useEffect, useMemo, useState } from "react";
import {
  Alert,
  App,
  Button,
  Card,
  Grid,
  List,
  Popconfirm,
  Space,
  Tag,
  Typography,
} from "antd";
import {
  CheckCircleOutlined,
  ExclamationCircleOutlined,
  SafetyOutlined,
  ReloadOutlined,
  ToolOutlined,
} from "@icons";

import { JobLogTail } from "../../../components/JobLogTail";
import {
  useRepairDiagnose,
  useRepairRun,
  useRepairStatus,
} from "../../../hooks/useSystemUpdates";

const { Text } = Typography;

interface RepairItem {
  ok: boolean;
  id: string;
  label: string;
  detail: string;
}

// parseDiagnose turns `jabali repair --diagnose` text into rows. Each issue
// is a `  ✓/✗ [id] label [destructive]` line followed by an indented status
// line (OK / BROKEN — detail). Unparseable output falls back to a raw block.
function parseDiagnose(text: string): RepairItem[] {
  const items: RepairItem[] = [];
  const lines = text.split("\n");
  for (let i = 0; i < lines.length; i++) {
    const m = lines[i].match(/^\s*([✓✗])\s+\[([^\]]+)\]\s+(.*)$/);
    if (!m) continue;
    const ok = m[1] === "✓";
    const id = m[2];
    const label = m[3].replace(/\s*\[destructive\]\s*$/, "").trim();
    const detail = (lines[i + 1] ?? "").trim();
    items.push({ ok, id, label, detail });
  }
  return items;
}

export function RepairCard() {
  const { t } = useTranslation();
  const { message } = App.useApp();
  const screens = Grid.useBreakpoint();
  const diagnose = useRepairDiagnose();
  const run = useRepairRun();
  const [since, setSince] = useState<string | null>(null);
  const status = useRepairStatus(since);

  const running = status.data?.status === "active" || status.data?.status === "activating";

  // When the auto-repair unit finishes, refresh the diagnosis so the list
  // reflects what's now fixed.
  const finished = since !== null && !running && !!status.data;
  useEffect(() => {
    if (finished) diagnose.mutate();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [finished]);

  const items = useMemo(
    () => (diagnose.data ? parseDiagnose(diagnose.data.output) : []),
    [diagnose.data],
  );
  const brokenCount = items.filter((i) => !i.ok).length;
  const parsedOk = items.length > 0;

  const onAutoRepair = async () => {
    try {
      const res = await run.mutateAsync();
      setSince(res.started_at);
      message.info("Auto-repair started");
    } catch (e: unknown) {
      message.error(e instanceof Error ? e.message : "auto-repair failed to start");
    }
  };

  const repairActions = (
    <Space wrap>
      <Button
        icon={<ReloadOutlined />}
        loading={diagnose.isPending}
        onClick={() => diagnose.mutate()}
      >
        Run Diagnostics
      </Button>
      <Popconfirm
        title={t("repaircard.auto_repair_safe_issues")}
        description={t("repaircard.applies_non_destructive_fixes_only_jabali_re")}
        okText={t("repaircard.run")}
        cancelText={t("repaircard.cancel")}
        onConfirm={onAutoRepair}
        disabled={running}
      >
        <Button
          type="primary"
          icon={<ToolOutlined />}
          loading={run.isPending || running}
          disabled={parsedOk && brokenCount === 0}
        >
          Auto-Repair safe issues
        </Button>
      </Popconfirm>
    </Space>
  );

  return (
    <Card
      title={
        <Space>
          <SafetyOutlined />
          Repair Center
        </Space>
      }
      extra={screens.md ? repairActions : undefined}
    >
      <Space direction="vertical" size={12} style={{ width: "100%" }}>
        {/* On narrow viewports the header `extra` overflows, so the actions
            move into the card body and wrap (Gitea #472). */}
        {!screens.md ? <div style={{ width: "100%" }}>{repairActions}</div> : null}
        <Text type="secondary">
          Detect and fix common install problems (git pointers, file
          permissions, stale units, AppArmor profiles, and more). Destructive
          fixes (re-clone, rebuilds) stay on the CLI: <Text code>jabali repair --all --yes</Text>.
        </Text>

        {!diagnose.data && !diagnose.isPending && (
          <Alert
            type="info"
            showIcon
            message={t("repaircard.run_diagnostics_to_see_what_s_broken")}
          />
        )}

        {diagnose.isError && (
          <Alert
            type="error"
            showIcon
            message={t("repaircard.diagnostics_failed")}
            description={
              diagnose.error instanceof Error ? diagnose.error.message : "Unknown error"
            }
          />
        )}

        {parsedOk && (
          <>
            {brokenCount === 0 ? (
              <Alert type="success" showIcon message={t("repaircard.no_issues_detected_all_healthy")} />
            ) : (
              <Alert
                type="warning"
                showIcon
                message={`${brokenCount} issue${brokenCount === 1 ? "" : "s"} detected`}
                description={t("repaircard.click_auto_repair_to_fix_the_non_destructive")}
              />
            )}
            <List
              size="small"
              bordered
              dataSource={items}
              renderItem={(it) => (
                <List.Item>
                  <Space align="start" style={{ width: "100%" }}>
                    {it.ok ? (
                      <Tag icon={<CheckCircleOutlined />} color="success">OK</Tag>
                    ) : (
                      <Tag icon={<ExclamationCircleOutlined />} color="error">Broken</Tag>
                    )}
                    <div style={{ minWidth: 0 }}>
                      <Text strong>{it.label}</Text>
                      {!it.ok && it.detail && (
                        <div>
                          <Text type="secondary" style={{ fontSize: 12 }}>{it.detail}</Text>
                        </div>
                      )}
                    </div>
                  </Space>
                </List.Item>
              )}
            />
          </>
        )}

        {/* Raw fallback when the output didn't parse into rows. */}
        {diagnose.data && !parsedOk && (
          <pre style={{ whiteSpace: "pre-wrap", margin: 0, fontSize: 12 }}>
            {diagnose.data.output}
          </pre>
        )}

        {since !== null && status.data && (
          <JobLogTail
            status={status.data.status}
            logTail={status.data.log_tail}
            exitCode={status.data.exit_code}
          />
        )}
      </Space>
    </Card>
  );
}
