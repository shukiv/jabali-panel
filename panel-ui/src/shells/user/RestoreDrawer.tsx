// RestoreDrawer — GH #267 Wave 4. Tenant self-service restore UI.
//
// Restores databases and/or the home directory from one of the caller's own
// backups (matches the backend). DB restore drops+reloads the chosen databases;
// home restore is additive (overwrites from the backup, never deletes files
// added since). Mail / DNS are not offered — see
// plans/m267-tenant-selective-restore.md (mail is RocksDB-unsafe via file apply;
// custom DNS records aren't captured).
import { useTranslation } from "react-i18next";
import { useEffect, useState } from "react";

import { Alert, Button, Checkbox, Drawer, Space, Spin, Typography } from "antd";
import { feedback } from "../../lib/feedback"; // GH #970: themed toasts

import { apiClient } from "../../apiClient";

interface ManifestStage {
  name: string;
  status: string;
  items?: string[];
}
interface ManifestResponse {
  kind: string;
  username: string;
  stages: ManifestStage[];
  dns_domains?: string[];
}
interface RestoreResult {
  applied?: string[] | null;
  skipped?: string[] | null;
  warnings?: string[] | null;
}

interface RestoreDrawerProps {
  backupId: string | null;
  open: boolean;
  onClose: () => void;
}

export const RestoreDrawer = ({ backupId, open, onClose }: RestoreDrawerProps) => {
  const { t } = useTranslation();
  const [loading, setLoading] = useState(false);
  const [databases, setDatabases] = useState<string[]>([]);
  const [selected, setSelected] = useState<string[]>([]);
  const [confirmOverwrite, setConfirmOverwrite] = useState(false);
  const [restoreHome, setRestoreHome] = useState(false);
  // GH #1044 item 3: only offer "Home directory" when the backup actually
  // CONTAINS a home stage. A database-only backup skips the home stage
  // (status=skipped), and offering the checkbox anyway invites a restore
  // selection that can never do anything.
  const [hasHome, setHasHome] = useState(false);
  const [mailboxes, setMailboxes] = useState<string[]>([]);
  const [selectedMb, setSelectedMb] = useState<string[]>([]);
  const [dnsDomains, setDnsDomains] = useState<string[]>([]);
  const [selectedDns, setSelectedDns] = useState<string[]>([]);
  // GH #1359: restore only specific domains' docroots (~/domains/<domain>)
  // instead of the whole home. Offered when the backup has a home stage.
  const [selectedDomainFiles, setSelectedDomainFiles] = useState<string[]>([]);
  const [submitting, setSubmitting] = useState(false);
  const [result, setResult] = useState<RestoreResult | null>(null);

  useEffect(() => {
    if (!open || !backupId) return;
    setLoading(true);
    setDatabases([]);
    setSelected([]);
    setConfirmOverwrite(false);
    setRestoreHome(false);
    setHasHome(false);
    setMailboxes([]);
    setSelectedMb([]);
    setDnsDomains([]);
    setSelectedDns([]);
    setSelectedDomainFiles([]);
    setResult(null);
    apiClient
      .get<ManifestResponse>(`/me/backups/${backupId}/manifest`)
      .then((resp) => {
        const dbs = (resp.data.stages ?? [])
          .filter((s) => s.name === "db" && s.status === "ok")
          .flatMap((s) => s.items ?? []);
        setDatabases(dbs);
        const mbs = (resp.data.stages ?? [])
          .filter((st) => st.name === "mail" && st.status === "ok")
          .flatMap((st) => st.items ?? []);
        setMailboxes(mbs);
        setDnsDomains(resp.data.dns_domains ?? []);
        setHasHome(
          (resp.data.stages ?? []).some(
            (st) => st.name === "home" && st.status === "ok",
          ),
        );
      })
      .catch((err) =>
        feedback.message.error(
          err instanceof Error ? err.message : "Could not read backup contents",
        ),
      )
      .finally(() => setLoading(false));
  }, [open, backupId]);

  // GH #1044: the restore runs as a background job now — the POST returns
  // 202 + job_id immediately (a large Postgres restore used to outlive the
  // HTTP request and surface as a bogus client-side timeout while the
  // server finished the work anyway). Poll the job row until it seals and
  // render the outcome it recorded.
  const handleRestore = async () => {
    if (
      !backupId ||
      (selected.length === 0 &&
        !restoreHome &&
        selectedDns.length === 0 &&
        selectedMb.length === 0 &&
        selectedDomainFiles.length === 0)
    )
      return;
    setSubmitting(true);
    setResult(null);
    try {
      const resp = await apiClient.post<{ job_id: string }>(
        `/me/backups/${backupId}/restore`,
        {
          databases: selected,
          home: restoreHome,
          mailboxes: selectedMb,
          dns_domains: selectedDns,
          domains: selectedDomainFiles,
          overwrite: true,
        },
      );
      const jobId = resp.data.job_id;
      feedback.message.info("Restore started — this can take a while for large backups");
      type JobRow = { id: string; status: string; warnings_json?: RestoreResult | null; error_text?: string };
      const started = Date.now();
      // Poll until the job seals. The server bounds the job at 60 min; the
      // drawer gives up politely a bit after that. Closing the drawer just
      // stops the polling — the job itself keeps running server-side and
      // stays visible in the backups list.
      for (;;) {
        await new Promise((r) => setTimeout(r, 3000));
        if (Date.now() - started > 65 * 60 * 1000) {
          feedback.message.warning("Still running — track it in the backups list");
          break;
        }
        const list = await apiClient.get<{ data: JobRow[] }>(`/me/backups`, { params: { limit: 50 } });
        const row = (list.data.data ?? []).find((j) => j.id === jobId);
        if (!row || row.status === "queued" || row.status === "running") continue;
        const outcome: RestoreResult = row.warnings_json ?? {};
        setResult(outcome);
        if (row.status === "succeeded") {
          const n = outcome.applied?.length ?? 0;
          if (n > 0) feedback.message.success(`Restored ${n} item${n === 1 ? "" : "s"}`);
          else feedback.message.warning("Nothing was restored — see details");
        } else {
          feedback.message.error("Restore failed — see details");
        }
        break;
      }
    } catch (err) {
      feedback.message.error(err instanceof Error ? err.message : "Restore failed");
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Drawer
      title={t("restoredrawer.restore_from_backup")}
      width={460}
      open={open}
      onClose={onClose}
      destroyOnClose
    >
      {loading ? (
        <div style={{ textAlign: "center", padding: 48 }}>
          <Spin />
        </div>
      ) : (
        <Space direction="vertical" size="middle" style={{ width: "100%" }}>
          <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
            Choose what to restore from this backup. Restoring a database
            <strong> replaces its contents</strong> with the backed-up copy.
            Restoring the home directory <strong>overwrites</strong> files from
            the backup but does not delete files you added since. Mail isn't
            restorable here.
          </Typography.Paragraph>

          {hasHome && (
            <Checkbox
              checked={restoreHome}
              onChange={(e) => setRestoreHome(e.target.checked)}
            >
              Home directory{" "}
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                (adds/overwrites files from the backup; does not delete files
                you added since)
              </Typography.Text>
            </Checkbox>
          )}

          {databases.length === 0 ? (
            <Typography.Text type="secondary">
              This backup contains no databases.
            </Typography.Text>
          ) : (
            <>
              <Typography.Text strong>Databases</Typography.Text>
              <Checkbox.Group
                value={selected}
                onChange={(v) => setSelected(v as string[])}
                style={{ display: "flex", flexDirection: "column", gap: 8 }}
                options={databases.map((d) => ({ label: d, value: d }))}
              />

            </>
          )}

          {mailboxes.length > 0 && (
            <>
              <Typography.Text strong>Mailboxes</Typography.Text>
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                Restores messages from the backup. Additive — existing messages
                are kept (deduped), nothing is deleted.
              </Typography.Text>
              <Checkbox.Group
                value={selectedMb}
                onChange={(v) => setSelectedMb(v as string[])}
                style={{ display: "flex", flexDirection: "column", gap: 8 }}
                options={mailboxes.map((m) => ({ label: m, value: m }))}
              />
            </>
          )}

          {dnsDomains.length > 0 && (
            <>
              <Typography.Text strong>DNS records</Typography.Text>
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                Replaces the domain&apos;s custom DNS records with the backed-up set
                (managed records are untouched).
              </Typography.Text>
              <Checkbox.Group
                value={selectedDns}
                onChange={(v) => setSelectedDns(v as string[])}
                style={{ display: "flex", flexDirection: "column", gap: 8 }}
                options={dnsDomains.map((d) => ({ label: d, value: d }))}
              />
            </>
          )}

          {/* GH #1359: restore one site's document root instead of the whole home. */}
          {hasHome && dnsDomains.length > 0 && (
            <>
              <Typography.Text strong>Domain files</Typography.Text>
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                Restores just these domains&apos; files (
                <code>~/domains/&lt;domain&gt;</code>) from the backup, over the
                live files — nothing is deleted. Use this to recover one site&apos;s
                document root without restoring the whole home directory.
              </Typography.Text>
              <Checkbox.Group
                value={selectedDomainFiles}
                onChange={(v) => setSelectedDomainFiles(v as string[])}
                style={{ display: "flex", flexDirection: "column", gap: 8 }}
                options={dnsDomains.map((d) => ({ label: d, value: d }))}
              />
            </>
          )}

          {(selected.length > 0 || restoreHome || selectedDns.length > 0 || selectedMb.length > 0 || selectedDomainFiles.length > 0) && (
            <Alert
              type="warning"
              showIcon
              message={t("restoredrawer.this_is_destructive")}
              description={t("restoredrawer.the_selected_items_will_be_overwritten_with")}
            />
          )}

          {(selected.length > 0 || restoreHome || selectedDns.length > 0 || selectedMb.length > 0 || selectedDomainFiles.length > 0) && (
            <>
              <Checkbox
                checked={confirmOverwrite}
                onChange={(e) => setConfirmOverwrite(e.target.checked)}
              >
                I understand this overwrites the selected item(s).
              </Checkbox>
              <Button
                type="primary"
                danger
                loading={submitting}
                disabled={!confirmOverwrite}
                onClick={handleRestore}
              >
                Restore selected
              </Button>
            </>
          )}

          {result && (
            <Alert
              type={(result.applied?.length ?? 0) > 0 ? "success" : "info"}
              showIcon
              message={t("restoredrawer.restore_result")}
              description={
                <Space direction="vertical" size={2}>
                  {(result.applied ?? []).map((a) => (
                    <span key={a}>✓ {a}</span>
                  ))}
                  {(result.skipped ?? []).map((s) => (
                    <span key={s}>• skipped: {s}</span>
                  ))}
                  {(result.warnings ?? []).map((w, i) => (
                    <Typography.Text type="secondary" key={i}>
                      {w}
                    </Typography.Text>
                  ))}
                </Space>
              }
            />
          )}
        </Space>
      )}
    </Drawer>
  );
};
