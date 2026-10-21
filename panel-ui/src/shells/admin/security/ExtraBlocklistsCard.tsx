// ExtraBlocklistsCard — the public IP blocklists (FireHOL levels 1-3) the
// admin can add to CrowdSec on top of its community blocklist, and the
// admin's own list URLs (https, up to 10; the server checks them). Saving only
// stores the choice; the reconciler hands it to the agent within a minute,
// and the agent downloads, filters and imports the lists in the background.
// The card polls the agent's last-run report while a run is going.
import { useEffect, useMemo, useState } from "react";
import { Alert, Button, Card, Checkbox, Input, Popconfirm, Space, Tag, Typography } from "antd";
import { ReloadOutlined } from "@icons";
import { useQuery, useQueryClient } from "@tanstack/react-query";

import { apiClient } from "../../../apiClient";
import { feedback } from "../../../lib/feedback";
import { Timestamp } from "../../../components/Timestamp";

const BASE = "/admin/security/crowdsec/blocklists/feeds";
const KEY = ["security", "crowdsec", "blocklist-feeds"] as const;

type ListView = { key: string; name: string; description: string; enabled: boolean };
type CustomView = { url: string; key: string };

// The server's limit on custom lists (internal/blocklists.MaxCustom).
const MAX_CUSTOM = 10;

type ListResult = {
  key: string;
  url?: string;
  enabled: boolean;
  at: string;
  fetched: number;
  imported: number;
  dropped_reserved: number;
  dropped_protected: number;
  dropped_crawler?: number; // absent from an agent older than the field
  dropped_duplicate: number;
  dropped_over_cap: number;
  invalid: number;
  error?: string;
};

type FeedsResponse = {
  lists: ListView[];
  custom?: CustomView[];
  status: { running: boolean; last_run?: string; lists: ListResult[] } | null;
};

const errorText = (err: unknown) => {
  const e = err as { response?: { data?: { detail?: string; error?: string } } };
  return e.response?.data?.detail ?? e.response?.data?.error ?? (err as Error).message;
};

// lastRunLine says what the last run did with one list that is on.
const lastRunLine = (r: ListResult) => {
  const crawler = r.dropped_crawler ?? 0;
  const left = r.dropped_reserved + r.dropped_protected + crawler + r.dropped_duplicate + r.dropped_over_cap;
  const parts = [`${r.imported.toLocaleString()} addresses or ranges banned`];
  if (left > 0) {
    const why = [
      r.dropped_reserved && `${r.dropped_reserved.toLocaleString()} private or reserved`,
      r.dropped_protected && `${r.dropped_protected.toLocaleString()} this server's or allowlisted`,
      crawler && `${crawler.toLocaleString()} Google's or Bing's crawlers`,
      r.dropped_duplicate && `${r.dropped_duplicate.toLocaleString()} already on another list`,
      r.dropped_over_cap && `${r.dropped_over_cap.toLocaleString()} over the limit`,
    ].filter(Boolean);
    parts.push(`${left.toLocaleString()} left out (${why.join(", ")})`);
  }
  return parts.join("; ");
};

export const ExtraBlocklistsCard = () => {
  const qc = useQueryClient();
  const q = useQuery<FeedsResponse>({
    queryKey: KEY,
    queryFn: async () => (await apiClient.get<FeedsResponse>(BASE)).data,
    refetchInterval: (query) => (query.state.data?.status?.running ? 10_000 : false),
  });
  const saved = useMemo(() => (q.data?.lists ?? []).filter((l) => l.enabled).map((l) => l.key), [q.data]);
  const savedCustom = useMemo(() => (q.data?.custom ?? []).map((c) => c.url), [q.data]);
  const [chosen, setChosen] = useState<string[]>([]);
  const [customText, setCustomText] = useState("");
  const [saving, setSaving] = useState(false);
  const [refreshing, setRefreshing] = useState(false);

  useEffect(() => {
    setChosen(saved);
  }, [saved]);
  useEffect(() => {
    setCustomText(savedCustom.join("\n"));
  }, [savedCustom]);

  const custom = [...new Set(customText.split("\n").map((l) => l.trim()).filter(Boolean))];
  const badCustom = custom.filter((u) => !/^https:\/\/[^\s/]+/.test(u));
  const customError =
    badCustom.length > 0
      ? `Not an https:// URL: ${badCustom.join(", ")}`
      : custom.length > MAX_CUSTOM
        ? `${custom.length} lists; at most ${MAX_CUSTOM}`
        : "";
  const customDirty = custom.length !== savedCustom.length || custom.some((u, i) => u !== savedCustom[i]);
  const dirty = chosen.length !== saved.length || chosen.some((k) => !saved.includes(k)) || customDirty;
  const adding = chosen.some((k) => !saved.includes(k)) || custom.some((u) => !savedCustom.includes(u));
  const results = new Map((q.data?.status?.lists ?? []).map((r) => [r.key, r]));
  const running = !!q.data?.status?.running;

  const save = async () => {
    setSaving(true);
    try {
      const { data } = await apiClient.put<FeedsResponse>(BASE, { enabled: chosen, custom });
      qc.setQueryData(KEY, data);
      feedback.message.success("Blocklists saved — applied within a minute");
      // The reconciler starts the run within a minute; look again then.
      window.setTimeout(() => qc.invalidateQueries({ queryKey: KEY }), 65_000);
    } catch (err) {
      feedback.message.error(`Failed to save: ${errorText(err)}`);
    } finally {
      setSaving(false);
    }
  };

  const refresh = async () => {
    setRefreshing(true);
    try {
      await apiClient.post(`${BASE}/refresh`);
      feedback.message.success("Downloading the lists again");
      await qc.invalidateQueries({ queryKey: KEY });
    } catch (err) {
      feedback.message.error(`Failed to start: ${errorText(err)}`);
    } finally {
      setRefreshing(false);
    }
  };

  return (
    <Card
      title="Extra blocklists"
      loading={q.isLoading}
      style={{ marginBottom: 16 }}
      extra={
        // Only the button: on a phone the header has no room for more beside the title.
        <Button
          size="small"
          icon={<ReloadOutlined />}
          loading={refreshing}
          disabled={(saved.length === 0 && savedCustom.length === 0) || dirty}
          onClick={refresh}
        >
          Download now
        </Button>
      }
    >
      <Space direction="vertical" size="middle" style={{ width: "100%" }}>
        <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
          CrowdSec already bans the addresses on its community blocklist. A list turned on here is
          banned too, on every port: websites, mail and SSH. Each list is downloaded again once a day.
          Private and reserved ranges, this server&apos;s own addresses, allowlisted addresses and
          Google&apos;s and Bing&apos;s crawlers are never banned. Turning a list off lifts its bans
          within a minute.
        </Typography.Paragraph>
        {(running || q.data?.status?.last_run) && (
          <Space size={12} wrap>
            {running && <Tag color="processing">Updating</Tag>}
            {q.data?.status?.last_run && (
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                Last run <Timestamp value={q.data.status.last_run} />
              </Typography.Text>
            )}
          </Space>
        )}
        {q.isError && <Alert type="error" showIcon message={`Could not load the lists: ${errorText(q.error)}`} />}
        {(q.data?.lists ?? []).map((l) => {
          const r = results.get(l.key);
          return (
            <div key={l.key}>
              <Checkbox
                checked={chosen.includes(l.key)}
                onChange={(e) =>
                  setChosen((prev) => (e.target.checked ? [...prev, l.key] : prev.filter((k) => k !== l.key)))
                }
              >
                <Typography.Text strong>{l.name}</Typography.Text>
              </Checkbox>
              <Typography.Paragraph type="secondary" style={{ margin: "2px 0 0 24px", fontSize: 13 }}>
                {l.description}
              </Typography.Paragraph>
              {l.enabled && r?.enabled && (
                <Typography.Paragraph
                  type={r.error ? "danger" : "secondary"}
                  style={{ margin: "2px 0 0 24px", fontSize: 12 }}
                >
                  {r.error
                    ? `Last run failed: ${r.error}. Bans from the run before stay until they expire.`
                    : lastRunLine(r)}
                </Typography.Paragraph>
              )}
            </div>
          );
        })}
        <div>
          <Typography.Text strong>Your own lists</Typography.Text>
          <Typography.Paragraph type="secondary" style={{ margin: "2px 0 6px", fontSize: 13 }}>
            One https:// URL per line, up to {MAX_CUSTOM}. Each must be a plain text list with one IP
            address or CIDR range per line (# comments are skipped). The same filters apply, and a list
            on a private or this server&apos;s own address is refused.
          </Typography.Paragraph>
          <Input.TextArea
            aria-label="Your own blocklist URLs"
            value={customText}
            onChange={(e) => setCustomText(e.target.value)}
            autoSize={{ minRows: 2, maxRows: 10 }}
            placeholder="https://www.spamhaus.org/drop/drop.txt"
            spellCheck={false}
            style={{ fontFamily: "monospace" }}
            status={customError ? "error" : undefined}
          />
          {customError && (
            <Typography.Text type="danger" style={{ fontSize: 12 }}>
              {customError}
            </Typography.Text>
          )}
          {(q.data?.custom ?? []).map((c) => {
            const r = results.get(c.key);
            if (!r) return null;
            return (
              <Typography.Paragraph
                key={c.key}
                type={r.error ? "danger" : "secondary"}
                style={{ margin: "4px 0 0", fontSize: 12, overflowWrap: "anywhere" }}
              >
                <Typography.Text code>{c.url}</Typography.Text>{" "}
                {r.error
                  ? `Last run failed: ${r.error}. Bans from the run before stay until they expire.`
                  : lastRunLine(r)}
              </Typography.Paragraph>
            );
          })}
        </div>
        <Space>
          {adding ? (
            <Popconfirm
              title="Ban these lists on every port?"
              description="Every address on a list you added is refused by the websites, mail and SSH. A visitor wrongly listed stays blocked until you turn the list off or allowlist them."
              okText="Save"
              onConfirm={save}
            >
              <Button type="primary" loading={saving} disabled={!!customError}>
                Save
              </Button>
            </Popconfirm>
          ) : (
            <Button type="primary" loading={saving} disabled={!dirty || !!customError} onClick={save}>
              Save
            </Button>
          )}
          {dirty && (
            <Button
              onClick={() => {
                setChosen(saved);
                setCustomText(savedCustom.join("\n"));
              }}
            >
              Reset
            </Button>
          )}
        </Space>
      </Space>
    </Card>
  );
};
