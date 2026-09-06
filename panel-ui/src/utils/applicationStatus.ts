// applicationStatus.ts — the shared install-status vocabulary for the
// Application Inventory (JAB-334). Admin and tenant application lists used to
// each define an identical status union, STATUS_META badge map, and
// TRANSITIONAL set, plus their own copies of "poll while transitional" and
// "STATUS_META[s] ?? pending". Those are unified here so the two lists can't
// drift on which states exist, how they render, or which ones keep polling.
//
// Icons are built with createElement so this stays a plain .ts util (no JSX)
// — a shared constants/functions module, not a component file.
import { createElement, type ReactNode } from "react";
import {
  LoadingOutlined,
  CheckCircleOutlined,
  ExclamationCircleOutlined,
} from "@icons";

export type ApplicationStatus =
  | "pending"
  | "installing"
  | "cloning"
  | "deleting"
  | "ready"
  | "failed";

export interface ApplicationStatusMeta {
  color: string;
  icon: ReactNode;
  label: string;
  spinning: boolean;
}

const spinner = createElement(LoadingOutlined, { spin: true });

export const APPLICATION_STATUS_META: Record<ApplicationStatus, ApplicationStatusMeta> = {
  pending:    { color: "default",    icon: spinner,                                 label: "Pending",    spinning: true  },
  installing: { color: "processing", icon: spinner,                                 label: "Installing", spinning: true  },
  cloning:    { color: "processing", icon: spinner,                                 label: "Cloning",    spinning: true  },
  deleting:   { color: "warning",    icon: spinner,                                 label: "Deleting",   spinning: true  },
  ready:      { color: "success",    icon: createElement(CheckCircleOutlined),      label: "Ready",      spinning: false },
  failed:     { color: "error",      icon: createElement(ExclamationCircleOutlined), label: "Failed",    spinning: false },
};

// The transitional states — a row in one of these is still settling, so the
// list keeps polling until it lands on ready/failed. One rule for both lists.
export const APPLICATION_TRANSITIONAL: ReadonlySet<ApplicationStatus> = new Set<ApplicationStatus>([
  "pending",
  "installing",
  "cloning",
  "deleting",
]);

export const isTransitionalStatus = (status: ApplicationStatus): boolean =>
  APPLICATION_TRANSITIONAL.has(status);

// anyTransitional — does any row still need the list to poll?
export const anyTransitional = (
  rows: ReadonlyArray<{ status: ApplicationStatus }>,
): boolean => rows.some((r) => isTransitionalStatus(r.status));

// applicationStatusMeta resolves a status to its badge metadata, falling back
// to the pending row for an unknown status (the `?? STATUS_META.pending` both
// lists used).
export const applicationStatusMeta = (status: ApplicationStatus): ApplicationStatusMeta =>
  APPLICATION_STATUS_META[status] ?? APPLICATION_STATUS_META.pending;
