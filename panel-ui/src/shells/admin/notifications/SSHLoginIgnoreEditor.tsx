// SSHLoginIgnoreEditor — GH #1310-adjacent ("drfeed spam"). A small tag-input
// under the "SSH login" event row that manages the per-account ignore list:
// successful logins by a listed username never notify (no immediate, no
// digest), so a disaster-recovery feed's SSH pull loop — or any noisy service
// account — can be silenced without turning SSH-login notifications off for
// everyone.
import { useState } from "react";
import { Select, Typography } from "antd";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import type { AxiosError } from "axios";

import { apiClient } from "../../../apiClient";
import { feedback } from "../../../lib/feedback";

const KEY = ["admin", "ssh-login-ignore"] as const;
const PATH = "/admin/settings/ssh-login-ignore";
const USERNAME_RE = /^[A-Za-z0-9._-]{1,64}$/;

export function SSHLoginIgnoreEditor() {
  const qc = useQueryClient();
  const [saving, setSaving] = useState(false);

  const q = useQuery<{ accounts: string[] }>({
    queryKey: KEY,
    queryFn: async () => {
      const { data } = await apiClient.get<{ accounts: string[] }>(PATH);
      return data;
    },
  });

  const accounts = q.data?.accounts ?? [];

  const save = async (next: string[]) => {
    // Client-side guard mirrors the server so an obviously bad tag never round-trips.
    const bad = next.find((a) => !USERNAME_RE.test(a));
    if (bad) {
      feedback.message.error(`Not a valid username: "${bad}"`);
      return;
    }
    setSaving(true);
    try {
      await apiClient.put(PATH, { accounts: next });
      await qc.invalidateQueries({ queryKey: KEY });
      feedback.message.success("Ignored accounts updated");
    } catch (err) {
      const detail = (err as AxiosError<{ detail?: string }>).response?.data?.detail;
      feedback.message.error(detail ?? "Could not update ignored accounts");
      await qc.invalidateQueries({ queryKey: KEY });
    } finally {
      setSaving(false);
    }
  };

  return (
    <div style={{ marginTop: 10, maxWidth: "64ch" }}>
      <Typography.Text strong style={{ fontSize: 12.5 }}>
        Ignored accounts
      </Typography.Text>
      <Typography.Paragraph type="secondary" style={{ margin: "2px 0 6px", fontSize: 12 }}>
        SSH logins by these usernames never notify — useful for a DR feed or backup
        loop that logs in constantly. Other accounts still notify normally.
      </Typography.Paragraph>
      <Select
        mode="tags"
        style={{ width: "100%" }}
        placeholder="e.g. drfeed"
        value={accounts}
        loading={q.isLoading || saving}
        disabled={q.isLoading || saving}
        tokenSeparators={[",", " "]}
        onChange={(next: string[]) => void save(next)}
        aria-label="SSH login ignored accounts"
      />
    </div>
  );
}
