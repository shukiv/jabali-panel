// backupArtifactActions — builds the shared Download / Restore / Delete
// RowActions for a backup-list row from its eligibility, so both the admin and
// tenant lists gate the three actions identically (JAB-332). The visibility of
// each action comes from `backupArtifactEligibility`; the per-screen deltas
// (which restore icon, the delete label + confirm copy, the callbacks that hit
// each screen's own resource path) are supplied by the caller.
//
// Screen-only actions (the admin Log and Cancel) are NOT built here: they live
// in the admin adapter, the same absence-not-a-flag split used for the Docker
// inventory's privileged verbs (JAB-335). The builder returns NAMED actions so
// each screen composes them in its own order:
//   tenant: [download, restore, delete]
//   admin : [download, log, restore, cancel, delete]
import { DownloadOutlined } from "@icons";
import type { ReactNode } from "react";

import type { RowAction } from "../RowActions";
import type { BackupArtifactEligibility } from "./backupArtifact";

export interface BackupArtifactActionPolicy {
  download: {
    /** dl.preparingId === row.id — swaps the label and shows the spinner. */
    preparing: boolean;
    onClick: () => void;
  };
  restore: {
    icon: ReactNode;
    onClick: () => void;
    confirm?: RowAction["confirm"];
  };
  delete: {
    icon: ReactNode;
    /** "Delete" for a backup, "Remove" for a restore-history row. */
    label: string;
    onClick: () => void;
    confirm: RowAction["confirm"];
  };
}

export interface BackupArtifactActions {
  download: RowAction;
  restore: RowAction;
  delete: RowAction;
}

export function backupArtifactActions(
  eligibility: BackupArtifactEligibility,
  policy: BackupArtifactActionPolicy,
): BackupArtifactActions {
  const preparing = policy.download.preparing;
  return {
    download: {
      key: "download",
      label: preparing ? "Preparing…" : "Download",
      icon: <DownloadOutlined />,
      loading: preparing,
      disabled: preparing,
      hidden: !eligibility.canDownload,
      onClick: policy.download.onClick,
    },
    restore: {
      key: "restore",
      label: "Restore",
      icon: policy.restore.icon,
      danger: true,
      hidden: !eligibility.canRestore,
      onClick: policy.restore.onClick,
      confirm: policy.restore.confirm,
    },
    delete: {
      key: "delete",
      label: policy.delete.label,
      icon: policy.delete.icon,
      danger: true,
      hidden: !eligibility.canDelete,
      onClick: policy.delete.onClick,
      confirm: policy.delete.confirm,
    },
  };
}
