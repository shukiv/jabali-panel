// UserDomainList — tenant view of the domains they own. JAB-300: a thin
// adapter over the shared <DomainInventory> module. This shell owns only the
// tenant-specific header: the title and the "Add Web Domain" button that opens
// the UserDomainDrawer. The table, columns, row actions and lifecycle live in
// the module, shared byte-for-byte with the admin list.
//
// GH #1541 (johnnyq): the old "Add" split (Web / DNS Zone / Mail Domain) is
// gone. Adding a website is the primary action, so it's a single "Add Web
// Domain" button; mail and DNS are opted into from inside that flow (checkboxes
// in the drawer) or added later from the Mail Domains / DNS Zones pages.
import { PlusSquareOutlined, GlobalOutlined } from "@icons";
import { Button, Card, Space, Typography } from "antd";
import { useState } from "react";

import { DomainInventory } from "../../../components/domains/DomainInventory";
import { UserDomainDrawer } from "./UserDomainDrawer";

export const UserDomainList = () => {
  const [drawerOpen, setDrawerOpen] = useState(false);

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
        <Button
          type="primary"
          icon={<PlusSquareOutlined />}
          onClick={() => setDrawerOpen(true)}
        >
          Add Web Domain
        </Button>
      </Space>

      <Card>
        <DomainInventory audience={{ kind: "tenant" }} />
      </Card>
      <UserDomainDrawer open={drawerOpen} onClose={() => setDrawerOpen(false)} mode="web" />
    </div>
  );
};
