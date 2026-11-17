// usePanelCertificate — TanStack Query for /admin/panel-certificate
// (M32, ADR-0105).
//
// Post-split the endpoint returns BOTH panel certs (kind=hostname and
// kind=mail), each with its own live routability decision. The UI
// renders a status row per kind; the single Use-LE/staging toggle
// (on the hostname row) governs both.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { apiClient } from "../apiClient";
import { isPanelRestartError } from "../apiErrors";

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
  // The certificate file this kind serves (GH #2166), or why there is none.
  served?: PanelServedCert | null;
  served_error?: string;
}

// PanelServedCert describes the certificate file a kind serves, read on each
// GET: the row's status can't tell a self-signed cert from a kept CA cert.
export interface PanelServedCert {
  type: "self_signed" | "lets_encrypt" | "lets_encrypt_staging" | "ca";
  issuer: string;
  subject: string;
  names: string[];
  not_before: string;
  not_after: string;
  fingerprint_sha256: string;
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

// Issuing the panel's certificate, or regenerating its self-signed one,
// restarts jabali-panel to serve it, and the refetch that follows lands in
// that restart. nginx answers 502 for its few seconds, so those are retried
// every 2 seconds for up to 30. Any other error gets the usual single retry.
const PANEL_RESTART_RETRIES = 15;
const PANEL_RESTART_RETRY_MS = 2000;

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
    retry: (failureCount, err) =>
      isPanelRestartError(err) ? failureCount < PANEL_RESTART_RETRIES : failureCount < 1,
    retryDelay: (failureCount, err) =>
      isPanelRestartError(err) ? PANEL_RESTART_RETRY_MS : Math.min(1000 * 2 ** failureCount, 30_000),
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
