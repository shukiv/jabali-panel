// A single database-user grant and its human label.
//
// GH #1415: `privileges` is a JSON array (Go []string) on the wire. It was
// once typed `string` here and grantLabel() called `.split(",")` on it,
// which threw on every "custom" grant (a set that is neither exactly ALL
// nor SELECT) and took the whole Databases page down via the error
// boundary. Kept in its own module so the label logic stays unit-testable
// without dragging in the AntD table.
export type Grant = {
  id: string;
  database_id: string;
  database_name: string;
  grant_level: "rw" | "ro" | "custom";
  privileges?: string[];
};

export function grantLabel(grant: Grant): string {
  switch (grant.grant_level) {
    case "rw":
      return "Full Access";
    case "ro":
      return "Read only";
    case "custom": {
      const privs = (grant.privileges ?? [])
        .map((p) => p.trim())
        .filter(Boolean);
      if (privs.length === 0) {
        return "Custom";
      }
      return privs.length > 2 ? `${privs.slice(0, 2).join(", ")}…` : privs.join(", ");
    }
    default:
      return "Unknown";
  }
}
