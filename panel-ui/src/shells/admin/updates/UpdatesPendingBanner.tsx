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
import dayjs from "dayjs";
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

// OsSecurityUpdatesBanner — JAB-353. A dashboard-level, persistent warning for
// the OS security-patch posture, distinct from the Jabali-panel-behind banner
// above (OS packages and the panel self-update are governed separately). It
// surfaces three cases, in severity order:
//   apt auto-updates OFF        — error. The host will not self-patch.
//   reboot required             — warning. Updates applied but not active.
//   security updates pending    — warning. Fixes are waiting to apply.
// Silent when auto-updates are on, nothing is pending, and no reboot is due.
export const OsSecurityUpdatesBanner = () => {
  const state = useUpdateState();
  const auto = useAutoupdateConfig();

  if (state.isLoading || state.isError || auto.isLoading) return null;

  const enabled = auto.data?.apt_enabled ?? true;
  const pendingSecurity = state.data?.apt_security ?? 0;
  const rebootRequired = state.data?.apt_reboot_required ?? false;
  const lastApplied = state.data?.apt_last_applied_at;

  if (enabled && pendingSecurity === 0 && !rebootRequired) return null;

  const disabled = !enabled;
  const message = disabled
    ? "OS security auto-updates are OFF"
    : rebootRequired
      ? "Reboot required to finish applying OS security updates"
      : `${pendingSecurity} OS security update${pendingSecurity === 1 ? "" : "s"} pending`;

  const patchAge = lastApplied
    ? `Last applied ${dayjs(lastApplied).format("MMM D, YYYY")}.`
    : "OS security patches have never been applied automatically on this host.";

  return (
    <Alert
      type={disabled ? "error" : "warning"}
      showIcon
      style={{ marginBottom: 16 }}
      message={message}
      description={
        <Space direction="vertical" size={8} style={{ width: "100%" }}>
          <Typography.Text>
            {disabled
              ? `This Internet-facing host will not apply OS security patches automatically${
                  pendingSecurity > 0 ? ` (${pendingSecurity} pending)` : ""
                }. ${patchAge} A Jabali panel update does not apply OS packages.`
              : rebootRequired
                ? `Security packages are installed but a reboot is needed to activate them (e.g. kernel/libc). ${patchAge}`
                : `${patchAge} They apply automatically in the maintenance window; review them now if urgent.`}
          </Typography.Text>
          <Link to="/jabali-admin/updates">
            <Button size="small" type="primary" danger={disabled}>
              Review OS updates
            </Button>
          </Link>
        </Space>
      }
    />
  );
};
