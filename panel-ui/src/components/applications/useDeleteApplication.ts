// useDeleteApplication — delete an install and invalidate the shared query
// keys, one path for both application lists (JAB-334 AC2). Tracks the in-flight
// id so a row can show a spinner. Uses the richer server-detail error
// extraction both lists want (the admin list previously surfaced only the
// transport message).
import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { apiClient } from "../../apiClient";
import { feedback } from "../../lib/feedback"; // GH #970: themed toasts
import {
  APPLICATION_INVALIDATION_KEYS,
  extractApiError,
  type ApplicationInstall,
} from "./applicationInventory";

type DeletableRow = Pick<ApplicationInstall, "id" | "domain_name" | "domain_id">;

export function useDeleteApplication() {
  const qc = useQueryClient();
  const [deletingId, setDeletingId] = useState<string | null>(null);

  const deleteApplication = async (row: DeletableRow): Promise<void> => {
    setDeletingId(row.id);
    try {
      await apiClient.delete(`/applications/${row.id}`);
      feedback.message.success(`Deleting ${row.domain_name || row.domain_id}…`);
      for (const key of APPLICATION_INVALIDATION_KEYS) {
        qc.invalidateQueries({ queryKey: [...key] });
      }
    } catch (err) {
      feedback.message.error(extractApiError(err, "Delete failed"));
    } finally {
      setDeletingId(null);
    }
  };

  return { deletingId, deleteApplication };
}
