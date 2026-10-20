// ExtraBlocklistsCard — the public IP blocklists (FireHOL levels 1-3) the
// admin can add to CrowdSec on top of its community blocklist. Saving only
// stores the choice; the reconciler hands it to the agent within a minute,
// and the agent downloads, filters and imports the lists in the background.
// The card polls the agent's last-run report while a run is going.
import { useEffect, useMemo, useState } from "react";
import { Alert, Button, Card, Checkbox, Popconfirm, Space, Tag, Typography } from "antd";
import { ReloadOutlined } from "@icons";
import { useQuery, useQueryClient } from "@tanstack/react-query";

import { apiClient } from "../../../apiClient";
import { feedback } from "../../../lib/feedback";
import { Timestamp } from "../../../components/Timestamp";

const BASE = "/admin/security/crowdsec/blocklists/feeds";
const KEY = ["security", "crowdsec", "blocklist-feeds"] as const;

type ListView = { key: string; name: string; description: string; enabled: boolean };

type ListResult = {
  key: string;
  enabled: boolean;
  at: string;
  fetched: number;
  imported: number;
  dropped_reserved: number;
  dropped_protected: number;
  dropped_duplicate: number;
  dropped_over_cap: number;
  invalid: number;
  error?: string;
};

type FeedsResponse = {
  lists: ListView[];
  status: { running: boolean; last_run?: string; lists: ListResult[] } | null;
};

const errorText = (err: unknown) => {
  const e = err as { response?: { data?: { detail?: string; error?: string } } };
  return e.response?.data?.detail ?? e.response?.data?.error ?? (err as Error).message;
};

// lastRunLine says what the last run did with one list that is on.
const lastRunLine = (r: ListResult) => {
  const left = r.dropped_reserved + r.dropped_protected + r.dropped_duplicate + r.dropped_over_cap;
  const parts = [`${r.imported.toLocaleString()} addresses or ranges banned`];
  if (left > 0) {
    const why = [
      r.dropped_reserved && `${r.dropped_reserved.toLocaleString()} private or reserved`,
      r.dropped_protected && `${r.dropped_protected.toLocaleString()} this server's or allowlisted`,
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
  const [chosen, setChosen] = useState<string[]>([]);
  const [saving, setSaving] = useState(false);
  const [refreshing, setRefreshing] = useState(false);

  useEffect(() => {
    setChosen(saved);
  }, [saved]);

  const dirty = chosen.length !== saved.length || chosen.some((k) => !saved.includes(k));
  const adding = chosen.some((k) => !saved.includes(k));
  const results = new Map((q.data?.status?.lists ?? []).map((r) => [r.key, r]));
  const running = !!q.data?.status?.running;

  const save = async () => {
    setSaving(true);
    try {
      const { data } = await apiClient.put<FeedsResponse>(BASE, { enabled: chosen });
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
        <Space size={12}>
          {running && <Tag color="processing">Updating</Tag>}
          {q.data?.status?.last_run && (
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              Last run <Timestamp value={q.data.status.last_run} />
            </Typography.Text>
          )}
          <Button
            size="small"
            icon={<ReloadOutlined />}
            loading={refreshing}
            disabled={saved.length === 0 || dirty}
            onClick={refresh}
          >
            Download now
          </Button>
        </Space>
      }
    >
      <Space direction="vertical" size="middle" style={{ width: "100%" }}>
        <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
          CrowdSec already bans the addresses on its community blocklist. A list turned on here is
          banned too, on every port: websites, mail and SSH. Each list is downloaded again once a day.
          Private and reserved ranges, this server&apos;s own addresses and allowlisted addresses are
          never banned. Turning a list off lifts its bans within a minute.
        </Typography.Paragraph>
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
        <Space>
          {adding ? (
            <Popconfirm
              title="Ban these lists on every port?"
              description="Every address on a list you added is refused by the websites, mail and SSH. A visitor wrongly listed stays blocked until you turn the list off or allowlist them."
              okText="Save"
              onConfirm={save}
            >
              <Button type="primary" loading={saving}>
                Save
              </Button>
            </Popconfirm>
          ) : (
            <Button type="primary" loading={saving} disabled={!dirty} onClick={save}>
              Save
            </Button>
          )}
          {dirty && <Button onClick={() => setChosen(saved)}>Reset</Button>}
        </Space>
      </Space>
    </Card>
  );
};
