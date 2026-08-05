// UpdatesPendingBanner — JAB-221.
//
// The Updates Center already knows the box is behind: the reconciler refreshes
// update_state every 6 hours. Nothing surfaced it outside that page, so two
// production incidents in three weeks came from boxes running builds that
// predated an already-merged fix — including one where a WooCommerce checkout
// WAF exclusion was inert for a day after merging, and real customers got
// banned mid-checkout.
//
// The non-obvious part is WHY being behind bites: config templates are embedded
// in the binary, so a host with an old binary actively REWRITES a fixed config
// back to the broken version on its next render. Being behind is not "missing
// out on new features", it is "a fix you already merged is being undone". The
// banner says that, because an operator who does not know it reads a
// commits-behind count as cosmetic.
//
// Two states, because the advice differs:
//   auto-update OFF — warning. Nothing will fix this on its own.
//   auto-update ON  — info. It converges tonight; say when, and stop nagging.
import { Alert, Button, Space, Typography } from "antd";
import { Link } from "react-router";
import { useTranslation } from "react-i18next";

import { useAutoupdateConfig, useUpdateState } from "../../../hooks/useSystemUpdates";

export const UpdatesPendingBanner = () => {
  const { t } = useTranslation();
  const state = useUpdateState();
  const auto = useAutoupdateConfig();

  const behind = state.data?.jabali_behind ?? 0;

  // Silent unless there is something to say. A dashboard banner that appears
  // while loading, or because a query failed, trains operators to dismiss it —
  // and then it is not there when it matters.
  if (state.isLoading || state.isError || behind <= 0) return null;

  const enabled = auto.data?.jabali_enabled ?? false;
  const time = auto.data?.jabali_time ?? "04:30";

  return (
    <Alert
      type={enabled ? "info" : "warning"}
      showIcon
      style={{ marginBottom: 16 }}
      message={
        enabled
          ? t("dashboard.updates_pending.title_auto", { count: behind })
          : t("dashboard.updates_pending.title", { count: behind })
      }
      description={
        <Space direction="vertical" size={8} style={{ width: "100%" }}>
          <Typography.Text>
            {enabled
              ? t("dashboard.updates_pending.body_auto", { time })
              : t("dashboard.updates_pending.body")}
          </Typography.Text>
          <Link to="/jabali-admin/updates">
            <Button size="small" type={enabled ? "default" : "primary"}>
              {enabled
                ? t("dashboard.updates_pending.action_auto")
                : t("dashboard.updates_pending.action")}
            </Button>
          </Link>
        </Space>
      }
    />
  );
};
