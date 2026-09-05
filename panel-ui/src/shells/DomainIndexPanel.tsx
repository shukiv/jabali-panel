// DomainIndexPanel — the body of the Index Manager (GH #1543). Extracted from
// DomainIndexButton so it renders both inline (a tab on the tenant Web Domain
// page) and inside the row-menu Modal, which now delegates to this panel. Picks
// which file(s) nginx serves as the default directory index; the value is an
// enum the agent maps to concrete `index ...;` directives.
import { useEffect, useState } from "react";
import { CheckOutlined } from "@icons";
import { Button, Radio, Typography } from "antd";
import { feedback } from "../lib/feedback";
import { useQueryClient } from "@tanstack/react-query";

import { apiClient } from "../apiClient";

export type IndexPriority =
  | "html_first"
  | "php_first"
  | "html_only"
  | "php_only"
  | "full";

export type DomainIndexTarget = {
  id: string;
  name: string;
  index_priority?: IndexPriority | string | null;
};

// Options in the order they appear in the user-facing radio group.
const options: { value: IndexPriority; label: string }[] = [
  { value: "php_first", label: "PHP first (index.php, then index.html)" },
  { value: "html_first", label: "HTML first (index.html, then index.php)" },
  { value: "php_only", label: "PHP only (index.php)" },
  { value: "html_only", label: "HTML only (index.html)" },
  { value: "full", label: "PHP, HTML, HTM (full support)" },
];

export const DomainIndexPanel = ({
  domain,
  onSaved,
}: {
  domain: DomainIndexTarget;
  onSaved?: () => void;
}) => {
  const qc = useQueryClient();
  const [saving, setSaving] = useState(false);
  const [value, setValue] = useState<IndexPriority>(
    (domain.index_priority as IndexPriority) || "html_first",
  );

  // Re-sync from the source when it changes underneath (another user's save, a
  // reconciler flip) so an inline pane that stays mounted doesn't clobber it.
  useEffect(() => {
    setValue((domain.index_priority as IndexPriority) || "html_first");
  }, [domain.id, domain.index_priority]);

  const handleSave = async () => {
    setSaving(true);
    try {
      await apiClient.patch(`/domains/${domain.id}`, { index_priority: value });
      feedback.message.success("Index priority saved");
      qc.invalidateQueries({ queryKey: ["list", "domains"] });
      qc.invalidateQueries({ queryKey: ["one", "domains", domain.id] });
      onSaved?.();
    } catch (err) {
      const e = err as { response?: { data?: { detail?: string } }; message?: string };
      feedback.message.error(`Failed to save: ${e.response?.data?.detail ?? e.message ?? "Unknown error"}`);
    } finally {
      setSaving(false);
    }
  };

  return (
    <div>
      <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
        Set the default directory index files
      </Typography.Paragraph>
      <Typography.Text strong>
        Directory Index Priority <Typography.Text type="danger">*</Typography.Text>
      </Typography.Text>
      <Radio.Group
        value={value}
        onChange={(e) => setValue(e.target.value)}
        style={{ display: "block", marginTop: 12 }}
      >
        {options.map((opt) => (
          <Radio
            key={opt.value}
            value={opt.value}
            style={{ display: "flex", alignItems: "flex-start", padding: "8px 0", whiteSpace: "normal" }}
          >
            <span style={{ display: "inline-block", lineHeight: 1.4 }}>{opt.label}</span>
          </Radio>
        ))}
      </Radio.Group>
      <Typography.Text type="secondary" style={{ display: "block", marginTop: 12 }}>
        Choose which file should be served as the default index
      </Typography.Text>
      <div style={{ marginTop: 16 }}>
        <Button type="primary" icon={<CheckOutlined />} loading={saving} onClick={handleSave}>
          Save
        </Button>
      </div>
    </div>
  );
};
