// useSettingsEmail — M6.4 Settings → Email card hook.
//
// Wire contract (verified against panel-api/internal/api/settings_email.go
// per feedback_verify_wire_contract.md). The endpoint returns two DIFFERENT
// shapes discriminated by HTTP status code, NOT by field presence:
//
//   200 OK   — panel-primary domain row exists, DKIM may or may not be
//              published depending on whether reconciler has converged:
//                { primary_domain_name, webmail_url, dkim_published,
//                  email_enabled_at,
//                  mail_hostname: { effective, applied },      (JAB-390)
//                  switchover: { desired, status, last_error,
//                                next_retry_at, updated_at } | null }
//   202 Accepted — row absent (install still converging, or pathological
//                  operator SQL delete): { primary_domain_name: null,
//                  status: "initializing" }
//
// The hook returns a discriminated union so the card component can pattern-
// match on `.state`. Converging installs get a short refetch interval so
// the UI flips to "Published" within a tick of the reconciler creating
// DKIM — no operator-initiated refresh required.

import { useMutation, useQuery, useQueryClient, type UseQueryResult } from "@tanstack/react-query";
import axios, { type AxiosError } from "axios";

import { apiClient } from "../apiClient";

// JAB-390: a requested change of the shared panel mail hostname. The
// reconciler applies it once the name points at this server and its
// certificate is issued; until then `applied` is unchanged.
export interface MailHostnameSwitchover {
  desired: string;
  status: "pending" | "issuing" | "failed" | "done";
  lastError: string;
  nextRetryAt: string | null;
  updatedAt: string;
}

export interface SettingsEmailReady {
  state: "ready";
  primaryDomainName: string;
  webmailURL: string;
  dkimPublished: boolean;
  emailEnabledAt: string | null;
  // effective: the name mail is served on; applied: the custom name in
  // effect, or null when the derived mail.<hostname> is.
  mailHostname: { effective: string; applied: string | null };
  switchover: MailHostnameSwitchover | null;
}

export interface SettingsEmailInitializing {
  state: "initializing";
}

export type SettingsEmail = SettingsEmailReady | SettingsEmailInitializing;

interface SwitchoverWire {
  desired: string;
  status: MailHostnameSwitchover["status"];
  last_error: string;
  next_retry_at: string | null;
  updated_at: string;
}

interface OkWire {
  primary_domain_name: string;
  webmail_url: string;
  dkim_published: boolean;
  email_enabled_at: string | null;
  // Absent on a server older than JAB-390.
  mail_hostname?: { effective: string; applied: string | null };
  switchover?: SwitchoverWire | null;
}

// webmailHost is the host of webmail_url, the effective mail hostname on a
// server that predates the mail_hostname field.
function webmailHost(url: string): string {
  try {
    return new URL(url).hostname;
  } catch {
    return "";
  }
}

interface InitWire {
  primary_domain_name: null;
  status: "initializing";
}

// Polls every 10s while initializing so the UI flips from "Initializing"
// to "Published" within one tick of the reconciler converging DKIM. Stops
// polling once state == "ready".
const INITIALIZING_REFETCH_MS = 10_000;
// Polls while a mail hostname change is pending or issuing, so the card
// shows it applied (or why it failed) without a manual refresh.
const SWITCHOVER_REFETCH_MS = 10_000;

const QK = ["settings", "email"] as const;

export function useSettingsEmail(): UseQueryResult<SettingsEmail, Error> {
  return useQuery<SettingsEmail, Error>({
    queryKey: QK,
    queryFn: async () => {
      try {
        const res = await apiClient.get<OkWire>("/admin/settings/email", {
          // axios throws on non-2xx by default; we treat 202 as success
          // via validateStatus so we can inspect the body without an
          // AxiosError detour.
          validateStatus: (s) => s === 200 || s === 202,
        });
        if (res.status === 202) {
          return { state: "initializing" };
        }
        const d = res.data;
        const sw = d.switchover ?? null;
        return {
          state: "ready",
          primaryDomainName: d.primary_domain_name,
          webmailURL: d.webmail_url,
          dkimPublished: d.dkim_published,
          emailEnabledAt: d.email_enabled_at,
          mailHostname: d.mail_hostname ?? { effective: webmailHost(d.webmail_url), applied: null },
          switchover: sw
            ? {
                desired: sw.desired,
                status: sw.status,
                lastError: sw.last_error,
                nextRetryAt: sw.next_retry_at,
                updatedAt: sw.updated_at,
              }
            : null,
        };
      } catch (err) {
        // Non-2xx-other-than-202 → surface to callers as error; axios
        // errors have a `.response` shape but we don't care about the
        // body here.
        if (axios.isAxiosError(err)) {
          const ax = err as AxiosError<{ error?: string }>;
          throw new Error(ax.response?.data?.error ?? ax.message);
        }
        throw err as Error;
      }
    },
    // Refetch every 10s if we're still initializing. React-query lets
    // refetchInterval be a function of the last result.
    refetchInterval: (query) => {
      const data = query.state.data;
      if (!data) return false;
      if (data.state === "initializing") return INITIALIZING_REFETCH_MS;
      const st = data.switchover?.status;
      return st === "pending" || st === "issuing" ? SWITCHOVER_REFETCH_MS : false;
    },
    staleTime: 30_000,
  });
}

// useRequestMailHostname asks the panel to move the shared mail hostname
// (PUT /admin/settings/email/mail-hostname). The request is recorded, not
// applied; switching back to mail.<hostname> is a request for that name.
export function useRequestMailHostname() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (mailHostname: string) => {
      await apiClient.put("/admin/settings/email/mail-hostname", { mail_hostname: mailHostname });
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: QK });
    },
  });
}

// useCancelMailHostname withdraws a pending or failed mail hostname change.
export function useCancelMailHostname() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async () => {
      await apiClient.delete("/admin/settings/email/mail-hostname");
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: QK });
    },
  });
}

// Widening helper used to derive bare union checks in tests without
// importing the state string literal.
export const INITIALIZING_STATE = "initializing" as const;
export const READY_STATE = "ready" as const;

// Wire shape union exported only for test fixtures to build realistic
// mocks; production code must not consume this directly.
export type SettingsEmailWire = OkWire | InitWire;
