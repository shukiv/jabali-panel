// BlockedUserAgentsCard — user agents every hosted site refuses with 403
// (server_settings.blocked_user_agents). One entry per line, matched as a
// case-insensitive part of the User-Agent header. The server checks the list
// again (useragents.Normalize) and the reconciler writes it into nginx
// within a minute. Only stops clients that name themselves; a bot that sends
// a browser user agent is CrowdSec's job (scenarios, AppSec, bot detection).
import { useEffect, useMemo, useState } from "react";
import { Alert, Button, Card, Input, Space, Typography } from "antd";
import { useQuery, useQueryClient } from "@tanstack/react-query";

import { apiClient } from "../../../apiClient";
import { feedback } from "../../../lib/feedback";

// Mirrors internal/useragents (MaxBlocked, the entry pattern).
export const MAX_BLOCKED_USER_AGENTS = 200;
const ENTRY = /^[A-Za-z0-9 ._/+-]{3,128}$/;

type Settings = { blocked_user_agents?: string };

const KEY = ["server-settings", "blocked-user-agents"] as const;

// entries returns the non-blank lines, trimmed.
const entries = (text: string): string[] =>
  text
    .split("\n")
    .map((l) => l.trim())
    .filter((l) => l !== "");

export const BlockedUserAgentsCard = () => {
  const qc = useQueryClient();
  const q = useQuery<Settings>({
    queryKey: KEY,
    queryFn: async () => (await apiClient.get<Settings>("/admin/settings")).data,
  });
  const saved = q.data?.blocked_user_agents ?? "";
  const [text, setText] = useState("");
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    setText(saved);
  }, [saved]);

  const list = useMemo(() => entries(text), [text]);
  const bad = list.filter((e) => !ENTRY.test(e));
  const tooMany = list.length > MAX_BLOCKED_USER_AGENTS;
  const dirty = text !== saved;

  const save = async () => {
    setSaving(true);
    try {
      const { data } = await apiClient.patch<Settings>("/admin/settings", { blocked_user_agents: text });
      qc.setQueryData(KEY, data);
      setText(data.blocked_user_agents ?? "");
      feedback.message.success("Blocked user agents saved — applied within a minute");
    } catch (err) {
      const e = err as { response?: { data?: { detail?: string; error?: string } } };
      feedback.message.error(
        `Failed to save: ${e.response?.data?.detail ?? e.response?.data?.error ?? (err as Error).message}`,
      );
    } finally {
      setSaving(false);
    }
  };

  return (
    <Card size="small" title="Blocked user agents (every site)" loading={q.isLoading} style={{ marginTop: 16 }}>
      <Space direction="vertical" size="middle" style={{ width: "100%" }}>
        <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
          Every hosted site answers 403 to a request whose User-Agent header contains one of these
          (case does not matter). One per line, for example <Typography.Text code>AhrefsBot</Typography.Text>{" "}
          or <Typography.Text code>SemrushBot</Typography.Text>. CrowdSec already bans the addresses of
          clients that use several hundred known bad user agents (scanners, attack tools, some crawlers);
          this list is for the ones you want turned away on top of that. It stops only clients that name
          themselves. Each site can also block AI crawlers on its own (domain Overview → Block AI
          crawlers).
        </Typography.Paragraph>
        <Input.TextArea
          aria-label="Blocked user agents"
          value={text}
          onChange={(e) => setText(e.target.value)}
          autoSize={{ minRows: 4, maxRows: 16 }}
          placeholder={"AhrefsBot\nSemrushBot"}
          spellCheck={false}
          style={{ fontFamily: "monospace" }}
        />
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          {list.length} of {MAX_BLOCKED_USER_AGENTS}. Letters, digits, spaces and . _ / + -, 3 to 128
          characters each. An entry that would also match a browser, Let&apos;s Encrypt or any Google
          or Bing crawler (for example <Typography.Text code>compatible</Typography.Text>,{" "}
          <Typography.Text code>bot</Typography.Text> or <Typography.Text code>Google</Typography.Text>)
          is refused: Google and Bing are never blocked.
        </Typography.Text>
        {bad.length > 0 && (
          <Alert
            type="error"
            showIcon
            message="Some entries cannot be used"
            description={bad.slice(0, 5).join(", ") + (bad.length > 5 ? ` and ${bad.length - 5} more` : "")}
          />
        )}
        {tooMany && (
          <Alert type="error" showIcon message={`At most ${MAX_BLOCKED_USER_AGENTS} entries`} />
        )}
        <Space>
          <Button
            type="primary"
            onClick={save}
            loading={saving}
            disabled={!dirty || bad.length > 0 || tooMany}
          >
            Save
          </Button>
          {dirty && <Button onClick={() => setText(saved)}>Reset</Button>}
        </Space>
      </Space>
    </Card>
  );
};
