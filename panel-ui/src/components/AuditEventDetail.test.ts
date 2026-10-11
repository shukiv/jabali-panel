// humanizeAction turns a recorded "METHOD /api/v1/..." into "Verb noun" for
// the Audit Log, the user's Account Activity and the admin Dashboard's
// Activity tab (GH #1249). The noun is the path's last segment, singular.
import { describe, expect, it } from "vitest";

import { humanizeAction, type AuditRow } from "./AuditEventDetail";

const row = (action: string) => ({ action }) as AuditRow;

describe("humanizeAction", () => {
  it.each([
    ["POST /api/v1/domains", "Create domain"],
    ["DELETE /api/v1/databases/:id", "Delete database"],
    ["POST /api/v1/admin/mail/batches", "Create batch"],
    ["POST /api/v1/domains/:id/mailboxes", "Create mailbox"],
    ["POST /api/v1/domains/:id/aliases", "Create alias"],
    ["POST /api/v1/admin/services/:name/processes", "Create process"],
    ["PUT /api/v1/admin/services", "Update service"],
    ["PUT /api/v1/admin/policies", "Update policy"],
    ["PUT /api/v1/domains/:id/dns", "Update dns"],
    ["PUT /api/v1/domains/:id/mta-sts", "Update mta sts"],
    ["PUT /api/v1/admin/security/egress", "Update egress"],
    ["PUT /api/v1/domains/:id/php-settings", "Update php setting"],
  ])("%s reads %s", (action, label) => {
    expect(humanizeAction(row(action))).toBe(label);
  });
});
