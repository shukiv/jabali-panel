// Maintenance tab — reclaim disk + sweep stale containers / images /
// volumes / build cache. Backed by docker.disk_usage + docker.prune
// agent verbs (see panel-agent/internal/commands/docker_lifecycle.go).
import { useTranslation } from "react-i18next";
import { App, Button, Card, Col, Row, Space, Statistic, Switch, Typography } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import { apiClient } from "../../../apiClient";
import { DockerEngineCard } from "./DockerEngineCard";

interface DiskUsage {
  images_bytes: number;
  containers_bytes: number;
  volumes_bytes: number;
  build_cache_bytes: number;
  reclaimable_bytes: number;
}

interface PruneResponse {
  reclaimed_bytes: number;
  raw: string;
}

function fmtBytes(n: number): string {
  if (n <= 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  let v = n;
  while (v >= 1000 && i < units.length - 1) {
    v /= 1000;
    i++;
  }
  return `${v.toFixed(v < 10 ? 2 : 1)} ${units[i]}`;
}

export const MaintenanceTab = () => {
  const { t } = useTranslation();
  const { message } = App.useApp();
  const qc = useQueryClient();
  const [pruneAll, setPruneAll] = useState(false);
  const [pruneVolumes, setPruneVolumes] = useState(false);

  const usage = useQuery({
    queryKey: ["docker-apps-disk-usage"],
    queryFn: async () => {
      const { data } = await apiClient.get<DiskUsage>("/admin/docker-apps/maintenance/disk-usage");
      return data;
    },
    refetchInterval: 30000,
  });

  const prune = useMutation({
    mutationFn: async (opts: { volumes: boolean; all: boolean }) => {
      const { data } = await apiClient.post<PruneResponse>(
        "/admin/docker-apps/maintenance/prune",
        opts,
      );
      return data;
    },
    onSuccess: (r) => {
      message.success(`Reclaimed ${fmtBytes(r.reclaimed_bytes)}`);
      qc.invalidateQueries({ queryKey: ["docker-apps-disk-usage"] });
    },
    onError: (e: unknown) =>
      message.error(e instanceof Error ? e.message : "Prune failed"),
  });

  const confirmPrune = () => {
    feedback.modal.confirm({
      title: "Run docker prune?",
      content: (
        <div>
          <Typography.Paragraph>
            Removes stopped containers, dangling images
            {pruneAll ? ", all unused images" : ""}
            {pruneVolumes ? ", and unused volumes (volume data is destroyed)" : ""}, plus the
            build cache.
          </Typography.Paragraph>
          {pruneVolumes && (
            <Typography.Text type="danger">
              Unused volume data will be deleted. Restic backups + bind mounts under
              /var/lib/jabali/docker-apps/ are unaffected (those are bind mounts, not docker
              volumes).
            </Typography.Text>
          )}
        </div>
      ),
      okText: "Prune",
      okButtonProps: { danger: pruneVolumes },
      onOk: () => prune.mutateAsync({ volumes: pruneVolumes, all: pruneAll }),
    });
  };

  return (
    <div>
      <DockerEngineCard />
      <Card title={t("maintenancetab.disk_usage")} loading={usage.isLoading} style={{ marginBottom: 16 }}>
        <Row gutter={[16, 16]}>
          <Col xs={12} sm={6}>
            <Statistic title={t("maintenancetab.images")} value={fmtBytes(usage.data?.images_bytes ?? 0)} />
          </Col>
          <Col xs={12} sm={6}>
            <Statistic title={t("maintenancetab.containers")} value={fmtBytes(usage.data?.containers_bytes ?? 0)} />
          </Col>
          <Col xs={12} sm={6}>
            <Statistic title={t("maintenancetab.volumes")} value={fmtBytes(usage.data?.volumes_bytes ?? 0)} />
          </Col>
          <Col xs={12} sm={6}>
            <Statistic title={t("maintenancetab.build_cache")} value={fmtBytes(usage.data?.build_cache_bytes ?? 0)} />
          </Col>
          <Col span={24}>
            <Statistic
              title={t("maintenancetab.reclaimable")}
              valueStyle={{ color: (usage.data?.reclaimable_bytes ?? 0) > 0 ? "#cf1322" : undefined }}
              value={fmtBytes(usage.data?.reclaimable_bytes ?? 0)}
            />
          </Col>
        </Row>
      </Card>

      <Card title={t("maintenancetab.prune")}>
        <Typography.Paragraph type="secondary">
          Frees disk by removing what docker considers unused. Toggle to control how aggressive.
        </Typography.Paragraph>
        <Space direction="vertical" size="middle" style={{ width: "100%" }}>
          <Space>
            <Switch checked={pruneAll} onChange={setPruneAll} />
            <span>
              <strong>Remove all unused images</strong> — not just dangling. Frees the most disk
              but next install pulls fresh.
            </span>
          </Space>
          <Space>
            <Switch checked={pruneVolumes} onChange={setPruneVolumes} />
            <span>
              <strong>Prune unused volumes</strong> — destructive. Only docker-managed volumes
              with no container attached. M48 catalog apps use bind mounts so this is safe for
              them.
            </span>
          </Space>
          <Button type="primary" loading={prune.isPending} onClick={confirmPrune}>
            Run prune
          </Button>
        </Space>
      </Card>
    </div>
  );
};
