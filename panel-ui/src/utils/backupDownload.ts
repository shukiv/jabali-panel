// GH #1408: a backup download runs a restic materialize (minutes) before the
// first byte, so a plain <a download> left the button looking dead. This hook
// drives the background prepare flow: POST prepare → poll prepare-status →
// trigger the browser download once the archive is warmed. Used by both the
// tenant (MyProfileBackupCard) and admin (AdminBackupsPage) Download actions.
import { useCallback, useEffect, useRef, useState } from "react";

import { apiClient } from "../apiClient";
import { downloadUrl } from "./download";
import { getActAs } from "../impersonation";
import { feedback } from "../lib/feedback";

const POLL_MS = 2000;
const MAX_POLLS = 900; // ~30 min ceiling, matching the server materialize budget

export function useBackupDownloadPrepare(scope: "me" | "admin") {
  const base = scope === "me" ? "/me/backups" : "/admin/backups";
  const [preparingId, setPreparingId] = useState<string | null>(null);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const cancelled = useRef(false);

  useEffect(
    () => () => {
      cancelled.current = true;
      if (timer.current) clearTimeout(timer.current);
    },
    [],
  );

  const triggerDownload = useCallback(
    (id: string) => {
      const act = scope === "me" ? getActAs() : null;
      downloadUrl(
        `/api/v1${base}/${id}/download${act ? `?act_as=${encodeURIComponent(act.id)}` : ""}`,
      );
    },
    [base, scope],
  );

  const start = useCallback(
    async (id: string) => {
      setPreparingId(id);
      feedback.message.info("Preparing download…");
      try {
        await apiClient.post(`${base}/${id}/download/prepare`);
      } catch {
        setPreparingId(null);
        feedback.message.error("Could not start the download");
        return;
      }
      let polls = 0;
      const poll = async () => {
        if (cancelled.current) return;
        polls += 1;
        try {
          const r = await apiClient.get<{ status: string; error?: string }>(
            `${base}/${id}/download/prepare-status`,
          );
          if (r.data.status === "ready") {
            setPreparingId(null);
            feedback.message.success("Download ready");
            triggerDownload(id);
            return;
          }
          if (r.data.status === "failed") {
            setPreparingId(null);
            feedback.message.error(r.data.error || "Failed to prepare the download");
            return;
          }
        } catch {
          // A transient 404 before the marker lands (or a blip) — keep polling.
        }
        if (polls >= MAX_POLLS) {
          setPreparingId(null);
          feedback.message.error("Preparing the download timed out — try again");
          return;
        }
        timer.current = setTimeout(poll, POLL_MS);
      };
      timer.current = setTimeout(poll, 1200);
    },
    [base, triggerDownload],
  );

  return { start, preparingId };
}
