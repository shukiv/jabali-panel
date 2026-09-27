// appsecExclusion — GH #1649. Turns a recent WAF block into the starting
// values of an operator CRS exclusion. The server validates every field
// (appseccfg.ValidateExclusion); these helpers only make the prefill match
// what it accepts, and the client rules catch the obvious mistakes early.
import type { AppSecBlockPattern } from "../../../hooks/useSecurityCrowdsec";

export type ExclusionFormValues = {
  host: string;
  uri_prefix: string;
  rule_id: string;
  note: string;
};

export type ExclusionPrefill = ExclusionFormValues & {
  /** The rules that scored on the block. Empty for a blank form. */
  ruleOptions: string[];
};

export const EMPTY_PREFILL: ExclusionPrefill = {
  host: "",
  uri_prefix: "",
  rule_id: "",
  note: "",
  ruleOptions: [],
};

/** What ValidateExclusion accepts for a host: [a-z0-9.-], at most 253. */
export const EXCLUSION_HOST_RE = /^[a-z0-9.-]{1,253}$/i;
/** A quote, backslash or newline would break the SecRule literal. */
export const EXCLUSION_PATH_FORBIDDEN_RE = /["\\\r\n]/;
export const EXCLUSION_NOTE_FORBIDDEN_RE = /["\r\n]/;
export const EXCLUSION_RULE_RE = /^[1-9][0-9]{0,7}$/;

/**
 * The host a block was sent to, as an exclusion stores it: lowercase, with
 * no port and no trailing dot. The Host header can carry either.
 */
export function hostForExclusion(host: string): string {
  let h = host.trim().toLowerCase();
  // An IPv6 literal ([::1]:443) is not a name an exclusion can scope to;
  // leave it for the operator to replace rather than guess.
  if (!h.startsWith("[")) h = h.replace(/:\d+$/, "");
  return h.replace(/\.$/, "");
}

/**
 * The path of a blocked URI, without its query string or fragment. The
 * exclusion matches it as a prefix, so the exact path is the narrowest start.
 */
export function pathForExclusion(uri: string): string {
  const path = uri.trim().split(/[?#]/, 1)[0] ?? "";
  if (path === "") return "/";
  return path.startsWith("/") ? path : `/${path}`;
}

/** The form values for excluding the rule behind a block pattern. */
export function prefillFromPattern(p: AppSecBlockPattern): ExclusionPrefill {
  const host = hostForExclusion(p.host);
  const path = pathForExclusion(p.uri);
  return {
    host,
    uri_prefix: path,
    rule_id: p.detections[0] ?? "",
    note: `False positive: ${p.count} block(s) from ${p.distinct_ips} IP(s)`,
    ruleOptions: [...p.detections],
  };
}
