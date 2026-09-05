// UserDomainList — tenant view of the domains they own. JAB-300: a thin
// adapter over the shared <DomainInventory> module. This shell owns only the
// tenant-specific header: the title and the "Add" split (Web / DNS Zone /
// Mail Domain) that opens the UserDomainDrawer. The table, columns, row
// actions and lifecycle live in the module, shared byte-for-byte with the
// admin list.
import { PlusSquareOutlined, GlobalOutlined } from "@icons";
import { Button, Card, Dropdown, Space, Typography } from "antd";
import { useState } from "react";

import { DomainInventory } from "../../../components/domains/DomainInventory";
import { UserDomainDrawer, type DomainDrawerMode } from "./UserDomainDrawer";
import { useServerCapabilities } from "../../../hooks/useServerCapabilities";

export const UserDomainList = () => {
  const { data: caps } = useServerCapabilities();
  const [drawerOpen, setDrawerOpen] = useState(false);
  // GH #1449: the same drawer serves three add-flows (web / dns-only / mail-only).
  const [drawerMode, setDrawerMode] = useState<DomainDrawerMode>("web");
  const openDrawer = (mode: DomainDrawerMode) => {
    setDrawerMode(mode);
    setDrawerOpen(true);
  };

  return (
    <div>
      <Space
        style={{
          marginBottom: 16,
          width: "100%",
          justifyContent: "space-between",
          flexWrap: "wrap",
          rowGap: 8,
        }}
      >
        <Typography.Title level={3} style={{ margin: 0 }}>
          <GlobalOutlined /> Domains
        </Typography.Title>
        {/* GH #1449: Web / DNS / Mail are independent services — offer each as
            its own add-flow. "Web Domain" keeps the full form (with Mail + DNS
            opt-outs); the other two add a docroot-less DNS-only zone or a
            mail-only domain. */}
        <Dropdown
          trigger={["click"]}
          menu={{
            // GH #1449 + #1417/#1419: only offer a service the server actually
            // runs — hide "DNS Zone" when the DNS module is off, "Mail Domain"
            // when mail is off (`!== false` treats the pre-load state as on).
            items: [
              { key: "web", label: "Web Domain" },
              ...(caps?.dns_enabled !== false ? [{ key: "dns", label: "DNS Zone" }] : []),
              ...(caps?.mail_enabled !== false ? [{ key: "mail", label: "Mail Domain" }] : []),
            ],
            onClick: ({ key }) => openDrawer(key as DomainDrawerMode),
          }}
        >
          <Button type="primary" icon={<PlusSquareOutlined />}>
            Add
          </Button>
        </Dropdown>
      </Space>

      <Card>
        <DomainInventory audience={{ kind: "tenant" }} />
      </Card>
      <UserDomainDrawer open={drawerOpen} onClose={() => setDrawerOpen(false)} mode={drawerMode} />
    </div>
  );
};
