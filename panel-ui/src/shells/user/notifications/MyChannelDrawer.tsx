// MyChannelDrawer — the tenant self-service notification-channel drawer. A thin
// adapter over the neutral ChannelDrawer Module (JAB-336, ADR-0083): it supplies
// the tenant policy (ownership-scoped resource + the effective creatable kinds,
// JAB-326) and re-exports the names its page and tests already import. All field
// rendering, validation and the write-only masked-secret affordance live in the
// Module.
import { ChannelDrawer } from "../../../components/notifications/ChannelDrawer";
import {
  TENANT_KINDS,
  tenantChannelPolicy,
  type NotificationChannel,
} from "../../../components/notifications/channelPolicy";
import type { ChannelKind } from "../../../utils/channelKindConfig";

export { TENANT_KINDS };
// MyChannel is the canonical channel row — kept as a named re-export so the
// tenant page keeps importing it from here.
export type MyChannel = NotificationChannel;

export interface MyChannelDrawerProps {
  open: boolean;
  onClose: () => void;
  existing?: MyChannel;
  /** Effective server allowlist (JAB-326). Falls back to the safe defaults. */
  allowedKinds?: ChannelKind[];
}

export function MyChannelDrawer({ open, onClose, existing, allowedKinds }: MyChannelDrawerProps) {
  return (
    <ChannelDrawer
      open={open}
      onClose={onClose}
      existing={existing}
      policy={tenantChannelPolicy(allowedKinds)}
    />
  );
}
