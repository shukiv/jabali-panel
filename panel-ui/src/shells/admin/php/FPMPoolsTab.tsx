// FPMPoolsTab — GH #339 phase 2, Step 3. Admin PHP Manager tab: pick a PHP
// version, see every user's pool on it, edit the raw pm.* via the existing
// PHPPoolEdit route. L3 (admin, uncapped). /php-pools is admin-only (PR #34).
import { useTranslation } from "react-i18next";
import { Alert, Select, Space, Spin, Table, Tag, Typography } from "antd";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { Link } from "react-router";
import { apiClient } from "../../../apiClient";

type Pool = {
  id: string;
  user_id: string;
  php_version: string;
  pm_mode: string;
  pm_max_children: number;
  performance_mode: string;
  status: string;
};

export const FPMPoolsTab = () => {
  const { t } = useTranslation();
  const [version, setVersion] = useState<string>("");
  const { data, isLoading, isError } = useQuery<Pool[]>({
    queryKey: ["admin-php-pools"],
    queryFn: async () => (await apiClient.get<{ data: Pool[] }>("/php-pools")).data.data ?? [],
  });

  if (isLoading) return <Spin />;
  if (isError) return <Alert type="error" message={t("fpmpoolstab.could_not_load_php_fpm_pools")} />;

  const pools = data ?? [];
  const versions = Array.from(new Set(pools.map((p) => p.php_version))).sort();
  const filtered = version ? pools.filter((p) => p.php_version === version) : pools;

  return (
    <Space direction="vertical" size="middle" style={{ width: "100%" }}>
      <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
        Raw per-user FPM pool tuning (admin, uncapped). For the tenant-facing safe
        tiers, set the package policy + the Performance Mode presets.
      </Typography.Paragraph>
      <Select
        style={{ width: 220 }}
        placeholder={t("fpmpoolstab.filter_by_php_version")}
        allowClear
        value={version || undefined}
        onChange={(v) => setVersion(v ?? "")}
        options={versions.map((v) => ({ value: v, label: `PHP ${v}` }))}
      />
      <Table<Pool>
        rowKey="id"
        dataSource={filtered}
        pagination={false}
        scroll={{ x: "max-content" }}
        columns={[
          { title: "PHP", dataIndex: "php_version" },
          { title: "PM", dataIndex: "pm_mode" },
          { title: "Max children", dataIndex: "pm_max_children" },
          { title: "Mode", dataIndex: "performance_mode" },
          {
            title: "Status",
            dataIndex: "status",
            render: (s: string) => <Tag color={s === "active" ? "green" : s === "error" ? "red" : "default"}>{s}</Tag>,
          },
          {
            title: "",
            key: "edit",
            render: (_: unknown, r: Pool) => <Link to={`/jabali-admin/php-pools/edit/${r.id}`}>Edit</Link>,
          },
        ]}
      />
    </Space>
  );
};
