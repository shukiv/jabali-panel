// validation.ts — shared form validation rules for Ant Design forms.
//
// These rules are defensive: they catch obvious junk client-side so the
// user gets instant feedback, but the API is still the authoritative
// validator (never trust the client).

import type { Rule } from "antd/es/form";

// ---------------------------------------------------------------------------
// XSS / injection guards
// ---------------------------------------------------------------------------

// NOTE: no /g flag on any regex below. `RegExp.test()` with /g is
// stateful — lastIndex persists across calls, so repeated validation
// on different values yields alternating true/false. Keep /i for
// case-insensitive matching only.
const XSS_PATTERNS = [
  /<script\b[^<]*(?:(?!<\/script>)<[^<]*)*<\/script>/i,
  /javascript:/i,
  /on\w+\s*=/i, // onclick=, onerror=, etc.
  /<iframe\b/i,
  /<object\b/i,
  /<embed\b/i,
];

function containsPattern(value: string, patterns: RegExp[]): boolean {
  return patterns.some((re) => re.test(value));
}

export const noXSS: Rule = {
  validator(_, value: unknown) {
    if (typeof value !== "string" || value === "") return Promise.resolve();
    if (containsPattern(value, XSS_PATTERNS)) {
      return Promise.reject(new Error("Contains disallowed characters or patterns"));
    }
    return Promise.resolve();
  },
};

/**
 * safeText is the XSS guard for free-form text inputs. Client-side
 * SQL-injection regexes were dropped: the patterns flagged legit
 * names ("Anne-Marie" typo'd as "Anne--Marie", surnames containing
 * a "#", names with "Drop") and SQL injection is a server-side
 * concern anyway — every panel-api repository call uses parameterized
 * queries via GORM and a client-side pre-filter adds zero defense.
 * Alias kept for backward compatibility with callers that imported
 * `safeText` before the split.
 */
export const safeText: Rule = noXSS;

// ---------------------------------------------------------------------------
// Common field validators
// ---------------------------------------------------------------------------

/** Domain name (RFC 1123 host). */
export const domain: Rule = {
  pattern: /^(?:[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)+(?:[a-zA-Z]{2,63}|[xX][nN]--[a-zA-Z0-9-]{1,59})$/,
  message: "Enter a valid domain name (e.g. example.com)",
};

/** Email address. */
export const email: Rule = {
  type: "email",
  message: "Must be a valid email",
};

/**
 * Linux username: must match the backend regex in panel-api's
 * internal/userops usernameRe — first char [a-z_], total length 1-32,
 * remaining chars from [a-z0-9_-]. Earlier pattern (3-32 chars, no
 * underscore/hyphen) rejected valid usernames the server accepts.
 */
export const linuxUsername: Rule = {
  pattern: /^[a-z_][a-z0-9_-]{0,31}$/,
  message: "1-32 chars, lowercase letters/digits/underscore/hyphen, must start with letter or underscore",
};

/** Database username segment (prepended automatically). */
export const dbUsername: Rule = {
  pattern: /^[a-z][a-z0-9_]{0,30}$/,
  message: "Lowercase letters, digits and underscores only; must start with a letter; max 30 chars",
};

/** Mailbox local part. */
export const mailboxLocalPart: Rule = {
  pattern: /^[a-z0-9][a-z0-9._+-]*$/i,
  message: "Letters, digits, dot/underscore/plus/hyphen only",
};

/** IPv4 or IPv6 address. */
export const ipAddress: Rule = {
  pattern:
    /^(?:(?:25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)\.){3}(?:25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)$|^([0-9a-fA-F:.]+)$/,
  message: "Enter a valid IP address",
};

/** Port number or range (e.g. 22 or 1000:2000). */
export const portOrRange: Rule = {
  pattern: /^\d+(:\d+)?$/,
  message: 'Number or "lo:hi" range',
};

/** Subdomain / path segment: letters, digits, hyphen, underscore. */
export const pathSegment: Rule = {
  pattern: /^[a-zA-Z0-9_-]+$/,
  message: "Letters, digits, hyphens and underscores only",
};

// ---------------------------------------------------------------------------
// Length helpers
// ---------------------------------------------------------------------------

export function maxLen(n: number): Rule {
  return { max: n, message: `Cannot exceed ${n} characters` };
}

export function minLen(n: number): Rule {
  return { min: n, message: `At least ${n} characters` };
}

// ---------------------------------------------------------------------------
// Composed rule sets for common form fields
// ---------------------------------------------------------------------------

/** Free-form text (name, description, etc.). */
export const textField = (opts?: { max?: number; required?: boolean }): Rule[] => {
  const rules: Rule[] = [];
  if (opts?.required) rules.push({ required: true, message: "This field is required" });
  rules.push(safeText);
  if (opts?.max) rules.push(maxLen(opts.max));
  return rules;
};

/** Email field with XSS protection. */
export const emailField = (opts?: { required?: boolean }): Rule[] => {
  const rules: Rule[] = [];
  if (opts?.required) rules.push({ required: true, message: "Email is required" });
  rules.push(email, noXSS);
  return rules;
};

/** Domain name field. */
export const domainField = (opts?: { required?: boolean }): Rule[] => {
  const rules: Rule[] = [];
  if (opts?.required) rules.push({ required: true, message: "Domain name is required" });
  rules.push(domain, noXSS, maxLen(253));
  return rules;
};

/** Username field. */
export const usernameField = (opts?: { required?: boolean }): Rule[] => {
  const rules: Rule[] = [];
  if (opts?.required) rules.push({ required: true, message: "Username is required" });
  rules.push(linuxUsername, noXSS);
  return rules;
};
