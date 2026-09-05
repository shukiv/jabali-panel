// AdminChannelDrawer — the server-wide notification-channel drawer. A thin
// adapter over the neutral ChannelDrawer Module (JAB-336, ADR-0083): it supplies
// the admin policy and re-exports the canonical row type its callers already
// import. All field rendering, validation and the write-only masked-secret
// affordance live in the Module.
import { ChannelDrawer } from "../../../components/notifications/ChannelDrawer";
import {
  ADMIN_CHANNEL_POLICY,
  type NotificationChannel,
} from "../../../components/notifications/channelPolicy";

export type { NotificationChannel };

export interface AdminChannelDrawerProps {
  open: boolean;
  onClose: () => void;
  /** Existing row for edit mode; undefined for create. */
  existing?: NotificationChannel;
}

export function AdminChannelDrawer({ open, onClose, existing }: AdminChannelDrawerProps) {
  return (
    <ChannelDrawer open={open} onClose={onClose} existing={existing} policy={ADMIN_CHANNEL_POLICY} />
  );
}
