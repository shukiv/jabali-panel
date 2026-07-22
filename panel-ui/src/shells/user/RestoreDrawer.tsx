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

import {
  Alert,
  Button,
  Checkbox,
  Drawer,
  Space,
  Spin,
  Typography,
  message,
} from "antd";

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
  const [mailboxes, setMailboxes] = useState<string[]>([]);
  const [selectedMb, setSelectedMb] = useState<string[]>([]);
  const [dnsDomains, setDnsDomains] = useState<string[]>([]);
  const [selectedDns, setSelectedDns] = useState<string[]>([]);
  const [submitting, setSubmitting] = useState(false);
  const [result, setResult] = useState<RestoreResult | null>(null);

  useEffect(() => {
    if (!open || !backupId) return;
    setLoading(true);
    setDatabases([]);
    setSelected([]);
    setConfirmOverwrite(false);
    setRestoreHome(false);
    setMailboxes([]);
    setSelectedMb([]);
    setDnsDomains([]);
    setSelectedDns([]);
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
      })
      .catch((err) =>
        message.error(
          err instanceof Error ? err.message : "Could not read backup contents",
        ),
      )
      .finally(() => setLoading(false));
  }, [open, backupId]);

  const handleRestore = async () => {
    if (!backupId || (selected.length === 0 && !restoreHome && selectedDns.length === 0 && selectedMb.length === 0)) return;
    setSubmitting(true);
    setResult(null);
    try {
      const resp = await apiClient.post<RestoreResult>(
        `/me/backups/${backupId}/restore`,
        { databases: selected, home: restoreHome, mailboxes: selectedMb, dns_domains: selectedDns, overwrite: true },
      );
      setResult(resp.data);
      const n = resp.data.applied?.length ?? 0;
      if (n > 0) message.success(`Restored ${n} item${n === 1 ? "" : "s"}`);
      else message.warning("Nothing was restored — see details");
    } catch (err) {
      message.error(err instanceof Error ? err.message : "Restore failed");
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

          <Checkbox
            checked={restoreHome}
            onChange={(e) => setRestoreHome(e.target.checked)}
          >
            Home directory{" "}
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              (adds/overwrites files from the backup; does not delete files you
              added since)
            </Typography.Text>
          </Checkbox>

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

          {(selected.length > 0 || restoreHome || selectedDns.length > 0 || selectedMb.length > 0) && (
            <Alert
              type="warning"
              showIcon
              message={t("restoredrawer.this_is_destructive")}
              description={t("restoredrawer.the_selected_items_will_be_overwritten_with")}
            />
          )}

          {(selected.length > 0 || restoreHome || selectedDns.length > 0 || selectedMb.length > 0) && (
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
