// DRStandbyBanner (GH #331 Step 3) — a persistent warning shown on every page
// when this box is a DR standby (read-only replica). It explains why mutations
// are refused (middleware.StandbyReadOnly 409s them) and points at the promote
// path. Rendered by both layouts; renders nothing on a primary.
import { Alert } from "antd";
import { useTranslation } from "react-i18next";

import { useServerCapabilities } from "../hooks/useServerCapabilities";

export function DRStandbyBanner() {
  const { t } = useTranslation();
  const { data: caps } = useServerCapabilities();
  if (!caps?.is_standby) {
    return null;
  }
  const peer = caps.dr_peer_label.trim();
  const description = peer
    ? t("dr.standby_banner_desc", { peer })
    : t("dr.standby_banner_desc_nopeer");
  return (
    <Alert
      type="warning"
      showIcon
      banner
      message={t("dr.standby_banner_title")}
      description={description}
      style={{ marginBottom: 16 }}
    />
  );
}
