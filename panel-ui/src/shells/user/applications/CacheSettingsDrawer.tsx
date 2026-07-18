// CacheSettingsDrawer — GH #616 + #612. Per-install cache tuning: page-cache URL
// exclusions (extra path prefixes that always bypass the nginx page cache, on
// top of the built-in wp-admin/cart/checkout set) PLUS the split behavioral
// controls (object-cache max-TTL, nginx page-cache TTL, Redis memory hint).
// Reads/writes GET/PUT /applications/:id/cache-settings; every value is
// panel-stamped as a JABALI_CACHE_* wp-config constant / domain setting by
// setCacheCore (the plugin never writes wp-config — GH #602). The reconciler
// re-renders the vhost and the agent re-stamps constants on save.
//
// A cookie bypass/allowlist was rejected: it would re-open the #416/#419
// fail-open class (cross-visitor content bleed on the shared microcache).
import { Alert, App, Button, Descriptions, Drawer, Form, InputNumber, Select, Switch, Tag } from "antd";
import { useEffect, useState } from "react";

import { apiClient } from "../../../apiClient";
import { extractApiError } from "../../../apiErrors";
import { StandardDrawerFooter } from "../../../components/StandardActionFooter";

type CacheSettings = {
  url_exclusions?: string[];
  object_cache_enabled?: boolean;
  page_cache_enabled?: boolean;
  page_ttl?: number;
  object_maxttl?: number;
  redis_maxmemory_mb?: number;
  profile?: string;
  auto_warm?: boolean;
  last_warm?: { at?: string; urls?: number; note?: string };
};
type CacheProfile = { key: string; label: string; warning: string };
type CacheStats = {
  connected?: boolean;
  hit_ratio?: number;
  keys?: number;
  used_memory?: number;
  driver?: string;
  page_cache?: { available?: boolean; hit_ratio?: number; total?: number; bypass?: number };
};
type GetResp = {
  cache_enabled: boolean;
  configured: boolean;
  settings: CacheSettings;
};

export function CacheSettingsDrawer({
  install,
  onClose,
}: {
  install: { id: string; name?: string; cache_enabled?: boolean } | null;
  onClose: () => void;
}) {
  const { message } = App.useApp();
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [urlExclusions, setUrlExclusions] = useState<string[]>([]);
  const [pageTtl, setPageTtl] = useState<number | null>(null);
  const [objectMaxTtl, setObjectMaxTtl] = useState<number | null>(null);
  const [redisMaxMemory, setRedisMaxMemory] = useState<number | null>(null);
  const [profile, setProfile] = useState<string>("");
  const [profiles, setProfiles] = useState<CacheProfile[]>([]);
  const [stats, setStats] = useState<CacheStats | null>(null);
  const [advising, setAdvising] = useState(false);
  const [advice, setAdvice] = useState<string | null>(null);
  const [objectOn, setObjectOn] = useState(true);
  const [pageOn, setPageOn] = useState(true);
  const [autoWarm, setAutoWarm] = useState(true);
  const [lastWarm, setLastWarm] = useState<{ at?: string; urls?: number; note?: string } | null>(null);
  // JAB-95 Phase 5: rich warmup run status.
  type WarmRun = {
    state: string;
    warmed: number;
    attempted: number;
    clamped_to: number;
    failed: number;
    first_error: string;
    finished_at?: string | null;
  };
  const [warmRun, setWarmRun] = useState<WarmRun | null>(null);
  const [warming, setWarming] = useState(false);

  useEffect(() => {
    if (!install) return;
    setLoading(true);
    apiClient
      .get<GetResp>(`/applications/${install.id}/cache-settings`)
      .then((res) => {
        const s = res.data.settings ?? {};
        setUrlExclusions(s.url_exclusions ?? []);
        setPageTtl(s.page_ttl ?? null);
        setObjectMaxTtl(s.object_maxttl ?? null);
        setRedisMaxMemory(s.redis_maxmemory_mb ?? null);
        setProfile(s.profile ?? "");
        setObjectOn(s.object_cache_enabled ?? true);
        setPageOn(s.page_cache_enabled ?? true);
        setAutoWarm(s.auto_warm ?? true);
        setLastWarm(s.last_warm ?? null);
      })
      .catch((e) =>
        message.error(extractApiError(e) ?? "Failed to load cache settings"),
      )
      .finally(() => setLoading(false));
    apiClient
      .get<{ profiles: CacheProfile[] }>(`/applications/cache-profiles`)
      .then((res) => setProfiles(res.data.profiles ?? []))
      .catch(() => setProfiles([]));
    apiClient
      .get<{ stats: CacheStats }>(`/applications/${install.id}/cache-stats`)
      .then((res) => setStats(res.data.stats ?? null))
      .catch(() => setStats(null));
    apiClient
      .get<{ run: WarmRun | null }>(`/applications/${install.id}/cache-warmup`)
      .then((res) => setWarmRun(res.data.run ?? null))
      .catch(() => setWarmRun(null));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [install]);

  const warmNow = async () => {
    if (!install) return;
    setWarming(true);
    try {
      await apiClient.post(`/applications/${install.id}/cache-warmup`);
      message.success("Warming started — refresh status in a moment.");
      // Poll the run a few seconds later so the fresh result shows.
      setTimeout(() => {
        apiClient
          .get<{ run: WarmRun | null }>(`/applications/${install.id}/cache-warmup`)
          .then((res) => setWarmRun(res.data.run ?? null))
          .catch(() => {});
      }, 4000);
    } catch (e) {
      message.error(extractApiError(e) ?? "Failed to start warmup");
    } finally {
      setWarming(false);
    }
  };

  const advise = async () => {
    if (!install) return;
    setAdvising(true);
    try {
      const res = await apiClient.post<{ recommended: { profile: string; note: string }; probe?: { ttfb_ms?: number } }>(
        `/applications/${install.id}/cache-advise`,
      );
      const r = res.data.recommended;
      setProfile(r.profile ?? "");
      setObjectOn(true);
      setPageOn(true);
      const ttfb = res.data.probe?.ttfb_ms ?? 0;
      setAdvice((r.note ?? "") + (ttfb > 0 ? `  Homepage TTFB: ${ttfb} ms.` : ""));
      message.success("Recommendation applied to the form — review and Save.");
    } catch (e) {
      message.error(extractApiError(e) ?? "Advisor failed");
    } finally {
      setAdvising(false);
    }
  };

  const save = async () => {
    if (!install) return;
    setSaving(true);
    try {
      await apiClient.put(`/applications/${install.id}/cache-settings`, {
        url_exclusions: urlExclusions,
        page_ttl: pageTtl ?? 0,
        object_maxttl: objectMaxTtl ?? 0,
        redis_maxmemory_mb: redisMaxMemory ?? 0,
        profile,
        object_cache_enabled: objectOn,
        page_cache_enabled: pageOn,
        auto_warm: autoWarm,
      });
      message.success("Cache settings saved — applied on the next reconcile (~60s).");
      onClose();
    } catch (e) {
      message.error(extractApiError(e) ?? "Failed to save cache settings");
    } finally {
      setSaving(false);
    }
  };

  const enabled = install?.cache_enabled ?? false;
  const activeProfile = profiles.find((p) => p.key === profile);

  return (
    <Drawer
      title={install ? `Cache settings — ${install.name ?? install.id}` : "Cache settings"}
      open={install !== null}
      onClose={onClose}
      width={520}
      footer={
        <StandardDrawerFooter
          primaryText="Save"
          primaryLoading={saving}
          onPrimary={() => void save()}
          onCancel={onClose}
        />
      }
    >
      {!enabled && (
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 16 }}
          message="Cache is off for this install"
          description="These settings are saved but only take effect once caching is enabled (the Cache toggle on the app row)."
        />
      )}
      {stats?.connected ? (
        <Descriptions
          size="small"
          bordered
          column={1}
          style={{ marginBottom: 16 }}
          title="Object cache (Redis)"
        >
          <Descriptions.Item label="Status">Connected</Descriptions.Item>
          <Descriptions.Item label="Client">{stats.driver ?? "-"}</Descriptions.Item>
          {stats.page_cache?.available && (stats.page_cache.total ?? 0) > 0 ? (
            <Descriptions.Item label="Page cache hit ratio (nginx)">
              {(stats.page_cache.hit_ratio ?? 0).toFixed(1)}% of {stats.page_cache.total} req
            </Descriptions.Item>
          ) : null}
          <Descriptions.Item label="Keys (this site)">{stats.keys ?? 0}</Descriptions.Item>
          {(stats.used_memory ?? 0) > 0 ? (
            <Descriptions.Item label="Hit ratio (server-wide)">
              {(stats.hit_ratio ?? 0).toFixed(1)}%
            </Descriptions.Item>
          ) : null}
          {(stats.used_memory ?? 0) > 0 ? (
            <Descriptions.Item label="Redis memory (server-wide)">
              {Math.round((stats.used_memory ?? 0) / (1024 * 1024))} MB
            </Descriptions.Item>
          ) : null}
        </Descriptions>
      ) : null}
      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 16 }}
        message="Applied on the next reconcile (~60s)"
        description="TTLs are stamped as wp-config constants / the domain page-cache TTL by the panel — the WordPress plugin never writes them. URL exclusions bypass the page cache on top of the built-in set (wp-admin, login, cart, checkout, my-account, the REST API)."
      />
      <Form layout="vertical" disabled={loading}>
        <Form.Item label="Object cache (Redis)" help="Persistent WordPress object cache. Only applies while the master Cache toggle is on.">
          <Switch checked={objectOn} onChange={setObjectOn} />
        </Form.Item>
        <Form.Item label="Page cache (nginx)" help="Full-page micro-cache for anonymous visitors. Independent of the object cache.">
          <Switch checked={pageOn} onChange={setPageOn} />
        </Form.Item>
        <Form.Item label="Auto-warm" help="Crawl the site to pre-populate the cache after enabling or purging.">
          <Switch checked={autoWarm} onChange={setAutoWarm} />
          {lastWarm?.at ? (
            <span style={{ marginLeft: 12, color: "#888" }}>
              Last warmed {new Date(lastWarm.at).toLocaleString()}
              {lastWarm.urls ? ` (${lastWarm.urls} URLs)` : ""}
            </span>
          ) : null}
        </Form.Item>
        <Form.Item label="Warmup" help="Crawl the site now and show the result.">
          <Button size="small" onClick={() => void warmNow()} loading={warming}>
            Warm now
          </Button>
          {warmRun ? (
            <div style={{ marginTop: 8 }}>
              <Tag
                color={
                  warmRun.state === "done"
                    ? "green"
                    : warmRun.state === "failed"
                      ? "red"
                      : "blue"
                }
              >
                {warmRun.state}
              </Tag>
              <span style={{ color: "#888" }}>
                warmed {warmRun.warmed}/{warmRun.attempted}
                {warmRun.clamped_to ? ` (limit ${warmRun.clamped_to})` : ""}
                {warmRun.failed ? `, ${warmRun.failed} failed` : ""}
              </span>
              {warmRun.first_error ? (
                <div style={{ color: "#c0392b", fontSize: 12, marginTop: 4 }}>
                  {warmRun.first_error}
                </div>
              ) : null}
            </div>
          ) : null}
        </Form.Item>
        <Form.Item label="Advisor">
          <Button onClick={() => void advise()} loading={advising}>
            Suggest settings from the site
          </Button>
        </Form.Item>
        {advice ? (
          <Alert
            type="success"
            showIcon
            style={{ marginBottom: 16 }}
            message="Recommendation"
            description={advice}
          />
        ) : null}
        <Form.Item
          label="Cache profile"
          help="Presets extra bypass paths for your site type, on top of the built-in wp-admin/cart/checkout set."
        >
          <Select
            value={profile}
            onChange={setProfile}
            options={profiles.map((p) => ({ value: p.key, label: p.label }))}
          />
        </Form.Item>
        {activeProfile?.warning ? (
          <Alert
            type="warning"
            showIcon
            style={{ marginBottom: 16 }}
            message={activeProfile.label}
            description={activeProfile.warning}
          />
        ) : null}
        <Form.Item
          label="Page-cache TTL (seconds)"
          help="How long nginx serves a cached page before revalidating. Blank = domain default. Range 10-86400."
        >
          <InputNumber
            min={10}
            max={86400}
            value={pageTtl}
            onChange={(v) => setPageTtl(v)}
            placeholder="default"
            style={{ width: "100%" }}
          />
        </Form.Item>
        <Form.Item
          label="Object-cache max-TTL (seconds)"
          help="Cap on Redis object-cache key lifetime. 0 = no expiry (rely on Redis LRU)."
        >
          <InputNumber
            min={0}
            max={2592000}
            value={objectMaxTtl}
            onChange={(v) => setObjectMaxTtl(v)}
            placeholder="0 (LRU)"
            style={{ width: "100%" }}
          />
        </Form.Item>
        <Form.Item
          label="Redis memory hint (MB)"
          help="Advisory per-tenant Redis memory ceiling. 0 = server default. Enforcement is best-effort."
        >
          <InputNumber
            min={0}
            max={65536}
            value={redisMaxMemory}
            onChange={(v) => setRedisMaxMemory(v)}
            placeholder="0 (default)"
            style={{ width: "100%" }}
          />
        </Form.Item>
        <Form.Item
          label="URL exclusions"
          help="Path prefixes that must never be page-cached (e.g. /private). Enter to add."
        >
          <Select
            mode="tags"
            value={urlExclusions}
            onChange={setUrlExclusions}
            tokenSeparators={[",", " "]}
            placeholder="/private"
          />
        </Form.Item>
      </Form>
    </Drawer>
  );
}
