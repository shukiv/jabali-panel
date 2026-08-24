// SSLManagerPage — the admin "Certificate console".
//
// Layout (2026-08 redesign, mockup-approved): four clickable summary
// tiles (health buckets, disjoint — see sslHealth.ts), the compact
// SYSTEM band for the panel's own certs (full management stays on
// Server Settings → Panel SSL), and one tabbed card holding the domain
// certificates table + the shared wildcard/multi-SAN certificates
// (JAB-170) that used to be a separate card below the fold.
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { useQuery } from "@tanstack/react-query";
import { Card, Tabs, Typography } from "antd";
import {
  ClockCircleOutlined,
  SafetyOutlined,
  ShieldCheckOutlined,
  WarningOutlined,
} from "@icons";

import { apiClient } from "../../../apiClient";
import { PanelSSLBand } from "../../../components/ssl/PanelSSLBand";
import { SSLManagerTable } from "../../../components/ssl/SSLManagerTable";
import { SharedCertificatesCard } from "../../../components/ssl/SharedCertificatesCard";
import {
  countBuckets,
  type SSLFilter,
  type SSLHealthRow,
} from "../../../components/ssl/sslHealth";

const ENDPOINT = "/admin/ssl-certificates";

interface TileSpec {
  key: Exclude<SSLFilter, "all" | "other">;
  label: string;
  color: string;
  bg: string;
  icon: React.ReactNode;
}

const TILES: TileSpec[] = [
  {
    key: "issued",
    label: "Issued",
    color: "#389e0d",
    bg: "rgba(82,196,26,0.14)",
    icon: <ShieldCheckOutlined />,
  },
  {
    key: "expiring",
    label: "Expiring ≤ 30 d",
    color: "#d46b08",
    bg: "rgba(250,140,22,0.14)",
    icon: <ClockCircleOutlined />,
  },
  {
    key: "failed",
    label: "Failed / retrying",
    color: "#cf1322",
    bg: "rgba(255,77,79,0.12)",
    icon: <WarningOutlined />,
  },
  {
    key: "self_signed",
    label: "Self-signed",
    color: "#6b7280",
    bg: "rgba(0,0,0,0.06)",
    icon: <SafetyOutlined />,
  },
];

interface CertRow extends SSLHealthRow {
  id: string;
}

export const SSLManagerPage = () => {
  const { t } = useTranslation();
  const [filter, setFilter] = useState<SSLFilter>("all");

  // Same queryKey as SSLManagerTable — one request feeds both the tiles
  // and the table via the react-query cache.
  const { data: certRows } = useQuery({
    queryKey: ["ssl-manager", ENDPOINT],
    queryFn: async () => {
      const response = await apiClient.get(ENDPOINT);
      return response.data.items as CertRow[];
    },
  });

  const counts = useMemo(() => {
    // Panel-cert rows live in the SYSTEM band, not the table — keep the
    // tile counts aligned with what the table below actually shows.
    const rows = (certRows ?? []).filter((r) => !r.id.startsWith("panel-cert:"));
    return countBuckets(rows);
  }, [certRows]);

  // Same queryKey as SharedCertificatesCard — cache-shared count for the
  // tab label.
  const { data: sharedCerts } = useQuery({
    queryKey: ["shared-certificates"],
    queryFn: async () => {
      const res = await apiClient.get("/admin/certificates/shared");
      return (res.data?.data ?? []) as { id: string }[];
    },
  });

  return (
    <div>
      <Typography.Title level={3} style={{ marginTop: 0, marginBottom: 16 }}>
        <ShieldCheckOutlined /> SSL Manager
      </Typography.Title>

      {/* Summary tiles double as filters: click to filter the table to
          that bucket, click again to clear. */}
      <div
        style={{
          display: "grid",
          gridTemplateColumns: "repeat(auto-fit, minmax(180px, 1fr))",
          gap: 16,
          marginBottom: 16,
        }}
      >
        {TILES.map((tile) => {
          const active = filter === tile.key;
          return (
            <Card
              key={tile.key}
              hoverable
              size="small"
              onClick={() => setFilter(active ? "all" : tile.key)}
              style={
                active
                  ? { borderColor: "#1677ff", boxShadow: "0 0 0 2px rgba(22,119,255,0.15)" }
                  : undefined
              }
              styles={{ body: { padding: 12 } }}
              aria-pressed={active}
              role="button"
            >
              <div style={{ display: "flex", alignItems: "center", gap: 10 }}>
                <div
                  style={{
                    flex: "0 0 auto",
                    width: 40,
                    height: 40,
                    borderRadius: 12,
                    background: tile.bg,
                    color: tile.color,
                    display: "flex",
                    alignItems: "center",
                    justifyContent: "center",
                    fontSize: 18,
                  }}
                >
                  {tile.icon}
                </div>
                <div style={{ minWidth: 0 }}>
                  <div style={{ color: tile.color, fontSize: 12, fontWeight: 600, marginBottom: 2 }}>
                    {tile.label}
                  </div>
                  <div style={{ fontSize: 20, fontWeight: 700, lineHeight: 1.1 }}>
                    {certRows ? counts[tile.key] : "—"}
                  </div>
                </div>
              </div>
            </Card>
          );
        })}
      </div>

      <div style={{ marginBottom: 16 }}>
        <PanelSSLBand />
      </div>

      <Card styles={{ body: { paddingTop: 8 } }}>
        <Tabs
          defaultActiveKey="domains"
          items={[
            {
              key: "domains",
              label: t("sslmanagerpage.domain_certificates"),
              children: (
                <SSLManagerTable
                  endpoint={ENDPOINT}
                  showOwner={true}
                  statusFilter={filter}
                  hideSystemRows
                />
              ),
            },
            {
              key: "shared",
              label: `Shared certificates${sharedCerts ? ` (${sharedCerts.length})` : ""}`,
              children: <SharedCertificatesCard embedded />,
            },
          ]}
        />
      </Card>
    </div>
  );
};
