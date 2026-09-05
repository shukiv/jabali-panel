// DomainList — admin domains grid. JAB-300: a thin adapter over the shared
// <DomainInventory> module. This shell owns only the admin-specific header:
// the title, the owner-scope chip + breadcrumbs (#483), and the Create button.
// The table, columns, row actions and lifecycle live in the module, shared
// byte-for-byte with the tenant list.
import { Button, Card, Space, Tag, Typography } from "antd";
import { GlobalOutlined, PlusOutlined } from "@icons";
import { useNavigate, useSearchParams } from "react-router";

import { useOneQuery } from "../../../hooks/useQueries";
import { useSetBreadcrumbs } from "../../../components/admin/BreadcrumbContext";
import { ownerResourceCrumbs, ownerLabel } from "../../../components/admin/entityLinks";
import { DomainInventory } from "../../../components/domains/DomainInventory";

export const DomainList = () => {
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();
  const ownerId = searchParams.get("user_id") ?? undefined;
  // Owner-scoped view (#483): fetch the owner so the breadcrumb + chip can
  // name them even when the filtered list is empty.
  const ownerQ = useOneQuery<{ id: string; username?: string | null }>({
    resource: "users",
    id: ownerId,
    enabled: !!ownerId,
  });
  const clearOwner = () => {
    const next = new URLSearchParams(searchParams);
    next.delete("user_id");
    next.delete("page");
    setSearchParams(next);
  };

  useSetBreadcrumbs(
    ownerId
      ? ownerResourceCrumbs({ id: ownerId, username: ownerQ.data?.username }, { key: "domains", label: "Domains" })
      : null,
  );

  const ownerRef = ownerId ? { id: ownerId, username: ownerQ.data?.username } : undefined;

  return (
    <div>
      <Space
        wrap
        align="center"
        style={{ marginBottom: 16, width: "100%", justifyContent: "space-between" }}
      >
        <Space wrap align="center">
          <Typography.Title level={3} style={{ margin: 0 }}>
            <GlobalOutlined /> Domains
          </Typography.Title>
          {ownerRef && (
            <Tag closable onClose={clearOwner} color="blue">
              Owner: {ownerLabel(ownerRef)}
            </Tag>
          )}
        </Space>
        <Button type="primary" icon={<PlusOutlined />} onClick={() => navigate("create")}>
          Create Domain
        </Button>
      </Space>

      <Card>
        <DomainInventory audience={{ kind: "admin", ownerId }} />
      </Card>
    </div>
  );
};
