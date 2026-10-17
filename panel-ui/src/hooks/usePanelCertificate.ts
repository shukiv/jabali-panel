// usePanelCertificate — TanStack Query for /admin/panel-certificate
// (M32, ADR-0105).
//
// Post-split the endpoint returns BOTH panel certs (kind=hostname and
// kind=mail), each with its own live routability decision. The UI
// renders a status row per kind; the single Use-LE/staging toggle
// (on the hostname row) governs both.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { apiClient } from "../apiClient";

export type PanelCertKind = "hostname" | "mail";

export interface PanelCertificate {
  kind: PanelCertKind;
  id: number;
  hostname: string;
  status:
    | "self_signed"
    | "pending_acme"
    | "issued"
    | "pending_acme_retry"
    | "failed";
  cert_pem_path: string;
  issued_at?: string | null;
  expires_at?: string | null;
  last_error?: string | null;
  attempt_count: number;
  next_retry_at?: string | null;
  staging: boolean;
  use_le: boolean;
  updated_at: string;
  // Live fields layered on by the GET handler — not stored.
  routable: boolean;
  routable_reason?: string;
}

// PanelSelfSignResult answers the hostname's Issue while Let's Encrypt is
// off (GH #2166): the panel regenerates its self-signed cert instead of
// asking Let's Encrypt. 202 regenerating, or 200 with nothing to do
// (expires_at is then the current cert's expiry).
export interface PanelSelfSignResult {
  self_signed: true;
  regenerating: boolean;
  detail: string;
  expires_at?: string;
}

export type PanelCertIssueResult = PanelCertificate | PanelSelfSignResult;

export function isPanelSelfSignResult(r: PanelCertIssueResult | undefined): r is PanelSelfSignResult {
  return !!r && (r as PanelSelfSignResult).self_signed === true;
}

interface PanelCertListResponse {
  certs: PanelCertificate[];
}

const QK = ["admin", "panel-certificate"] as const;

export function usePanelCertificate() {
  return useQuery<PanelCertificate[]>({
    queryKey: QK,
    queryFn: async () => {
      const r = await apiClient.get<PanelCertListResponse>(
        "/admin/panel-certificate",
      );
      return r.data?.certs ?? [];
    },
    refetchInterval: 30_000,
    refetchIntervalInBackground: false,
  });
}

export function usePanelCertificateToggle() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (patch: { use_le?: boolean; staging?: boolean }) => {
      const r = await apiClient.post<PanelCertificate>(
        "/admin/panel-certificate/toggle",
        patch,
      );
      return r.data;
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: QK });
    },
  });
}

export function usePanelCertificateIssue() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (kind: PanelCertKind = "hostname") => {
      const r = await apiClient.post<PanelCertIssueResult>(
        `/admin/panel-certificate/issue?kind=${encodeURIComponent(kind)}`,
      );
      return r.data;
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: QK });
    },
  });
}
