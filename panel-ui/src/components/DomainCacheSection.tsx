// DomainCacheSection — per-domain nginx FastCGI micro-cache toggle + TTL
// (ADR-0108, Gitea #596). Self-contained like DomainEmailSection: loads its own
// state, flips the switch, edits the cache TTL (DB-as-truth → reconciler
// re-renders the vhost), and exposes a manual purge button.
import { useTranslation } from "react-i18next";
import { useCallback, useEffect, useState } from "react";
import {
  Alert,
  Button,
  InputNumber,
  Popconfirm,
  Select,
  Skeleton,
  Space,
  Switch,
  message,
} from "antd";
import { CheckOutlined, CloseOutlined, ThunderboltOutlined } from "@icons";

import { apiClient } from "../apiClient";

type CacheState = {
  domain_id: string;
  domain_name: string;
  enabled: boolean;
  ttl_seconds: number;
  cache_query_allowlist?: string[];
};

type Props = { domainId: string };

const TTL_MIN = 10;
const TTL_MAX = 86400;
const QALLOW_MAX = 8;
const QALLOW_NAME_RE = /^[a-z0-9_]{1,32}$/;

// arraysEqual — order-insensitive compare so the Save button only lights up on a
// real change (the API stores the list sorted).
const sameSet = (a: string[], b: string[]) =>
  a.length === b.length && [...a].sort().join(",") === [...b].sort().join(",");

export const DomainCacheSection = ({ domainId }: Props) => {
  const { t } = useTranslation();
  const [state, setState] = useState<CacheState | null>(null);
  const [loading, setLoading] = useState(true);
  const [toggling, setToggling] = useState(false);
  const [purging, setPurging] = useState(false);
  const [ttl, setTtl] = useState<number>(600);
  const [savingTtl, setSavingTtl] = useState(false);
  const [qAllow, setQAllow] = useState<string[]>([]);
  const [savingQAllow, setSavingQAllow] = useState(false);

  const fetchState = useCallback(async () => {
    setLoading(true);
    try {
      const res = await apiClient.get<CacheState>(`/domains/${domainId}/cache`);
      setState(res.data);
      setTtl(res.data.ttl_seconds ?? 600);
      setQAllow(res.data.cache_query_allowlist ?? []);
    } catch {
      message.error("Failed to load cache status");
    } finally {
      setLoading(false);
    }
  }, [domainId]);

  useEffect(() => {
    fetchState();
  }, [fetchState]);

  const onFlip = async (next: boolean) => {
    setToggling(true);
    try {
      const res = await apiClient.put<CacheState>(`/domains/${domainId}/cache`, {
        enabled: next,
      });
      setState(res.data);
      setTtl(res.data.ttl_seconds ?? 600);
      message.success(
        next
          ? "Caching enabled — applying to the site's nginx config"
          : "Caching disabled",
      );
    } catch {
      message.error("Failed to toggle caching");
      await fetchState();
    } finally {
      setToggling(false);
    }
  };

  const onSaveTtl = async () => {
    setSavingTtl(true);
    try {
      const res = await apiClient.put<CacheState>(`/domains/${domainId}/cache`, {
        enabled: !!state?.enabled,
        ttl_seconds: ttl,
      });
      setState(res.data);
      setTtl(res.data.ttl_seconds ?? 600);
      message.success(`Cache TTL set to ${res.data.ttl_seconds}s — re-rendering nginx`);
    } catch {
      message.error("Failed to update cache TTL");
      await fetchState();
    } finally {
      setSavingTtl(false);
    }
  };

  const onSaveQAllow = async () => {
    const cleaned = Array.from(
      new Set(qAllow.map((s) => s.trim().toLowerCase()).filter(Boolean)),
    );
    const bad = cleaned.find((n) => !QALLOW_NAME_RE.test(n));
    if (bad) {
      message.error(`Invalid param "${bad}": use lower-case letters, digits, underscore (max 32 chars)`);
      return;
    }
    if (cleaned.length > QALLOW_MAX) {
      message.error(`Too many params (${cleaned.length}); max ${QALLOW_MAX}`);
      return;
    }
    setSavingQAllow(true);
    try {
      const res = await apiClient.put<CacheState>(`/domains/${domainId}/cache`, {
        enabled: !!state?.enabled,
        cache_query_allowlist: cleaned,
      });
      setState(res.data);
      setQAllow(res.data.cache_query_allowlist ?? []);
      message.success(
        cleaned.length
          ? "Cacheable query params saved — re-rendering nginx"
          : "Query-param caching disabled",
      );
    } catch {
      message.error("Failed to save cacheable query params");
      await fetchState();
    } finally {
      setSavingQAllow(false);
    }
  };

  const onPurge = async () => {
    setPurging(true);
    try {
      await apiClient.post(`/domains/${domainId}/cache/purge`);
      message.success("Cache purged");
    } catch {
      message.error("Failed to purge cache");
    } finally {
      setPurging(false);
    }
  };

  if (loading) return <Skeleton active paragraph={{ rows: 1 }} />;

  const enabled = !!state?.enabled;
  const ttlDirty = enabled && ttl !== (state?.ttl_seconds ?? 600);
  const qAllowDirty = enabled && !sameSet(qAllow, state?.cache_query_allowlist ?? []);

  return (
    <Space direction="vertical" style={{ width: "100%" }}>
      <Space size="middle" align="center" wrap>
        <Switch
          checkedChildren={<CheckOutlined />}
          unCheckedChildren={<CloseOutlined />}
          checked={enabled}
          loading={toggling}
          onChange={onFlip}
        />
        <span>nginx page cache</span>
        <Popconfirm
          title={t("domaincachesection.purge_cached_pages_for_this_domain")}
          okText={t("domaincachesection.purge")}
          onConfirm={onPurge}
          disabled={!enabled}
        >
          <Button
            icon={<ThunderboltOutlined />}
            loading={purging}
            disabled={!enabled}
          >
            Purge cache
          </Button>
        </Popconfirm>
      </Space>

      <Space size="small" align="center" wrap>
        <span>Cache TTL:</span>
        <InputNumber
          min={TTL_MIN}
          max={TTL_MAX}
          step={30}
          value={ttl}
          disabled={!enabled || savingTtl}
          onChange={(v) => setTtl(typeof v === "number" ? v : 600)}
          addonAfter="sec"
          style={{ width: 140 }}
        />
        <Button
          type="primary"
          size="small"
          loading={savingTtl}
          disabled={!ttlDirty}
          onClick={onSaveTtl}
        >
          Save TTL
        </Button>
      </Space>

      <Space size="small" align="start" wrap>
        <span style={{ lineHeight: "32px" }}>Cacheable query params:</span>
        <Select
          mode="tags"
          value={qAllow}
          onChange={(v) => setQAllow(v as string[])}
          disabled={!enabled || savingQAllow}
          placeholder="e.g. paged"
          tokenSeparators={[",", " "]}
          style={{ minWidth: 220 }}
          open={false}
          suffixIcon={null}
        />
        <Button
          type="primary"
          size="small"
          loading={savingQAllow}
          disabled={!qAllowDirty}
          onClick={onSaveQAllow}
        >
          Save params
        </Button>
      </Space>

      <Alert
        type="info"
        showIcon
        title={t("domaincachesection.cacheable_query_parameters")}
        description={t("domaincachesection.query_string_params_that_get_their_own_cache")}
      />

      <Alert
        type="info"
        showIcon
        title={t("domaincachesection.page_cache_with_background_refresh")}
        description={t("domaincachesection.caches_php_html_responses_for_the_ttl_above")}
      />
    </Space>
  );
};
