import { useTranslation } from "react-i18next";
import { useEffect, useState } from "react";
import {
  Alert,
  Button,
  Card,
  Checkbox,
  Space,
  Table,
  Typography,
  notification,
} from "antd";
import { SaveOutlined } from "@icons";

import { apiClient } from "../../../apiClient";

// DNS record-type permissions for non-admin tenants (GH #466, ADR-0150).
// Admins always have full control; this matrix governs only regular users.
// NS/SOA are zone infrastructure and stay admin-only, so they are not shown.

const MANAGED_TYPES = ["A", "AAAA", "CNAME", "MX", "TXT", "SRV", "CAA"] as const;
type RecordType = (typeof MANAGED_TYPES)[number];
type Op = "create" | "edit" | "delete";
type Perm = { create: boolean; edit: boolean; delete: boolean };
type Policy = Record<string, Perm>;

const EMPTY_PERM: Perm = { create: false, edit: false, delete: false };
const FULL_PERM: Perm = { create: true, edit: true, delete: true };

function normalize(p: Policy | undefined): Policy {
  const out: Policy = {};
  for (const t of MANAGED_TYPES) {
    const v = p?.[t];
    out[t] = v ? { create: !!v.create, edit: !!v.edit, delete: !!v.delete } : { ...EMPTY_PERM };
  }
  return out;
}

// Client-side preset expansion mirrors models.DNSPolicyPreset so the table
// reflects exactly what the server would store. The Save still PUTs the
// resolved matrix.
function applyPreset(name: "permissive" | "hosting-safe" | "locked-down"): Policy {
  const out: Policy = {};
  for (const t of MANAGED_TYPES) out[t] = { ...FULL_PERM };
  if (name === "hosting-safe") {
    out["CAA"] = { ...EMPTY_PERM };
  } else if (name === "locked-down") {
    for (const t of MANAGED_TYPES) out[t] = { ...EMPTY_PERM };
    out["A"] = { ...FULL_PERM };
    out["AAAA"] = { ...FULL_PERM };
    out["CNAME"] = { ...FULL_PERM };
  }
  return out;
}

export function DNSPermissionsCard() {
  const { t } = useTranslation();
  const [policy, setPolicy] = useState<Policy>(() => normalize(undefined));
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const resp = await apiClient.get<{ dns_user_record_policy?: Policy }>("/admin/settings");
        if (!cancelled) setPolicy(normalize(resp.data.dns_user_record_policy));
      } catch {
        if (!cancelled) {
          notification.error({ message: "Failed to load DNS permissions" });
        }
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  const toggle = (type: RecordType, op: Op, checked: boolean) => {
    setPolicy((prev) => ({ ...prev, [type]: { ...prev[type], [op]: checked } }));
  };

  const onSave = async () => {
    setSaving(true);
    try {
      const resp = await apiClient.patch<{ dns_user_record_policy?: Policy }>("/admin/settings", {
        dns_user_record_policy: policy,
      });
      setPolicy(normalize(resp.data.dns_user_record_policy));
      notification.success({ message: "DNS permissions saved" });
    } catch {
      notification.error({ message: "Failed to save DNS permissions" });
    } finally {
      setSaving(false);
    }
  };

  const columns = [
    { title: "Record type", dataIndex: "type", key: "type", render: (t: string) => <Typography.Text code>{t}</Typography.Text> },
    ...(["create", "edit", "delete"] as Op[]).map((op) => ({
      title: op.charAt(0).toUpperCase() + op.slice(1),
      key: op,
      render: (_: unknown, row: { type: RecordType }) => (
        <Checkbox
          checked={policy[row.type][op]}
          onChange={(e) => toggle(row.type, op, e.target.checked)}
        />
      ),
    })),
  ];

  return (
    <Card title={t("dnspermissionscard.dns_record_permissions")} style={{ marginBottom: 16 }} loading={loading}>
      <Alert
        type="info"
        style={{ marginBottom: 16 }}
        message={t("dnspermissionscard.which_dns_record_types_regular_users_may_man")}
        description={t("dnspermissionscard.admins_always_have_full_control_ns_and_soa_r")}
      />
      <Space wrap style={{ marginBottom: 16 }}>
        <Button onClick={() => setPolicy(applyPreset("permissive"))}>Permissive</Button>
        <Button onClick={() => setPolicy(applyPreset("hosting-safe"))}>Hosting-safe</Button>
        <Button onClick={() => setPolicy(applyPreset("locked-down"))}>Locked-down</Button>
      </Space>
      <Table
        size="small"
        rowKey="type"
        pagination={false}
        columns={columns}
        dataSource={MANAGED_TYPES.map((t) => ({ type: t }))}
        scroll={{ x: "max-content" }}
      />
      <Space style={{ marginTop: 16 }}>
        <Button type="primary" icon={<SaveOutlined />} loading={saving} onClick={onSave}>
          Save DNS permissions
        </Button>
      </Space>
    </Card>
  );
}
