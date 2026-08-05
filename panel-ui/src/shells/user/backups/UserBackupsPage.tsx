// User-shell Backups page — lifts the MyProfileBackupCard into its
// own first-class /jabali-panel/backups route. Operator/tenant
// requested the card sit alongside Files / DNS / SSL in the
// sidebar instead of being buried inside the Profile page.
import { Typography } from "antd";

import { SaveOutlined } from "@icons";

import { MyProfileBackupCard } from "../MyProfileBackupCard";
import { BackupSchedulesCard } from "../BackupSchedulesCard";

export const UserBackupsPage = () => {
  return (
    <div>
      <Typography.Title level={3} style={{ marginTop: 0, marginBottom: 16 }}>
        <SaveOutlined /> Backup / Restore
      </Typography.Title>
      <MyProfileBackupCard />
      <BackupSchedulesCard />
    </div>
  );
};
