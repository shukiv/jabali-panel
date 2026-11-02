// Confirm text for moving a domain's mail away from this server: email turned
// off (DELETE /domains/:id/email, the tenant Disable) or a mail provider other
// than Jabali mail.
//
// Since PR #2350 such a domain is not local any more. Mail for it that reaches
// this server without signing in is refused (550 5.7.1), and the server's own
// mail to it (websites, other accounts) goes to the domain's MX. Its mailboxes
// are kept: they still sign in, keep their stored mail and can send, but new
// mail for them no longer arrives here. All three entry points say the same.
import { Typography } from "antd";
import type { ReactNode } from "react";

import { apiClient } from "../../apiClient";

/**
 * Mailboxes on the domain, from the same list the mailbox pages show (the
 * hidden noreply@ relay is not counted, GH #1056). Null when the count can't be
 * read: callers then ask anyway, so a failed request never skips the warning.
 */
export async function countDomainMailboxes(domainId: string): Promise<number | null> {
  try {
    const { data } = await apiClient.get<{ total?: number }>(
      `/domains/${domainId}/mailboxes?page=1&page_size=1`,
    );
    return typeof data?.total === "number" ? data.total : null;
  } catch {
    return null;
  }
}

function mailboxesLeftText(domainLabel: string, count: number | null): string | null {
  if (count === 0) return null;
  if (count === null) {
    return `Mailboxes on ${domainLabel} are kept. They can still sign in, read their mail and send, but new mail for them no longer arrives here.`;
  }
  if (count === 1) {
    return `1 mailbox on ${domainLabel} is kept. It can still sign in, read its mail and send, but new mail for it no longer arrives here.`;
  }
  return `${count} mailboxes on ${domainLabel} are kept. They can still sign in, read their mail and send, but new mail for them no longer arrives here.`;
}

export function mailAwayContent({
  domainLabel,
  mailboxCount,
  dnsRemoved = false,
  footer,
}: {
  /** The domain name, or "this domain" where the name isn't at hand. */
  domainLabel: string;
  /** From countDomainMailboxes or a list row; null = unknown. */
  mailboxCount: number | null;
  /** Email off removes the managed mail DNS records; a provider change replaces them. */
  dnsRemoved?: boolean;
  footer?: string;
}): ReactNode {
  const left = mailboxesLeftText(domainLabel, mailboxCount);
  return (
    <>
      <Typography.Paragraph>
        {`Mail for ${domainLabel} no longer arrives on this server. Mail that reaches this server for it is refused, and mail sent from this server (websites, other accounts) goes to the domain's MX record.`}
        {dnsRemoved ? " The managed mail DNS records are removed." : ""}
      </Typography.Paragraph>
      {left && <Typography.Paragraph strong>{left}</Typography.Paragraph>}
      {footer && <Typography.Paragraph style={{ marginBottom: 0 }}>{footer}</Typography.Paragraph>}
    </>
  );
}
