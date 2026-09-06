// useChannelActions — the shared toggle / delete / test handlers for a
// notification-channel inventory (JAB-336, ADR-0083). The admin tab and the
// tenant page render different table shells but drive the same three actions
// against their own resource path (policy.resourcePath, AC2). The delete path
// takes an optional onDeleted hook so the tenant page can invalidate its event
// routes (a deleted channel unpicks itself from the routing matrix).
import { apiClient } from "../../apiClient";
import { feedback } from "../../lib/feedback"; // GH #970: themed toasts
import { useDeleteMutation, useUpdateMutation } from "../../hooks/useQueries";
import type { ChannelPolicy, NotificationChannel } from "./channelPolicy";

// sendChannelTest — POST /:id/test and toast the outcome. Shared by the inventory
// "Test" action and the drawer's "Send test" button so there is one test path.
export async function sendChannelTest(
  policy: ChannelPolicy,
  row: { id: string; name: string },
): Promise<void> {
  try {
    const res = await apiClient.post<{ delivered?: boolean }>(
      `/${policy.resourcePath}/${row.id}/test`,
    );
    feedback.message.success(policy.testResult(row.name, res.data?.delivered));
  } catch (err) {
    // A synchronous send surfaces the real delivery error (e.g. SMTP auth).
    feedback.message.error(err instanceof Error ? err.message : "Test failed");
  }
}

export function useChannelActions(policy: ChannelPolicy, opts?: { onDeleted?: () => void }) {
  const update = useUpdateMutation<NotificationChannel, { enabled: boolean }>({
    resource: policy.resourcePath,
  });
  const remove = useDeleteMutation({ resource: policy.resourcePath });

  const toggleEnabled = async (row: NotificationChannel, next: boolean) => {
    try {
      await update.mutateAsync({ id: row.id, input: { enabled: next } });
    } catch (err) {
      feedback.message.error(err instanceof Error ? err.message : "Toggle failed");
    }
  };

  const deleteChannel = async (row: NotificationChannel) => {
    try {
      await remove.mutateAsync({ id: row.id });
      feedback.message.success(`Deleted ${row.name}`);
      opts?.onDeleted?.();
    } catch (err) {
      feedback.message.error(err instanceof Error ? err.message : "Delete failed");
    }
  };

  const testChannel = (row: NotificationChannel) => sendChannelTest(policy, row);

  return { toggleEnabled, deleteChannel, testChannel };
}
