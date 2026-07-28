// useSupport — TanStack mutation for the diagnostic-report endpoint.
//
// POST /admin/support/diagnostic →
//   agent collects + redacts + uploads to enclosed.jabali-panel.com
//   returns {url, password, note_id, ...}
//
// Operator forwards URL+password to support via mailto: link built
// client-side (DiagnosticReportModal). No notify endpoint needed.
import { useMutation } from "@tanstack/react-query";

import { apiClient } from "../apiClient";

export interface DiagnosticReport {
  url: string;
  password: string;
  note_id: string;
  byte_count: number;
  generated_at: string;
  redaction_count: number;
  file_count: number;
  // Short, public-safe hand-off code (GH #357 claim-code). Present only when the
  // operator configured a support-claim service (JABALI_CLAIM_URL); empty
  // otherwise, in which case the link + password are the hand-off.
  claim_code?: string;
}

export function useDiagnosticReport() {
  return useMutation<DiagnosticReport>({
    mutationFn: async () => {
      const r = await apiClient.post<DiagnosticReport>("/admin/support/diagnostic");
      return r.data;
    },
  });
}
