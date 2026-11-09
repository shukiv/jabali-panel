import { useEffect, useState } from "react";
import { Button, Card, InputNumber, Space, Typography } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts

import { apiClient } from "../../../apiClient";
import { MAX_REDIRECT_RECIPIENTS } from "../../../hooks/useMailGroups";

// The panel refuses a cap above this (mailgroupops.MaxExternalMemberCap): each
// outside address is one redirect from the list.
export const MAX_MAIL_LIST_EXTERNAL_MEMBER_CAP = MAX_REDIRECT_RECIPIENTS;

// MailListsCard — Server Settings → Email: how many addresses outside this
// server one distribution list may deliver to (GH #1833). Default 20, the
// most; 0 lets no list hold any. Lowering it leaves the addresses lists
// already hold.
export const MailListsCard = () => {
  const [cap, setCap] = useState<number | null>(null);
  const [saved, setSaved] = useState<number | null>(null);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const resp = await apiClient.get<{ mail_list_external_member_cap?: number }>("/admin/settings");
        const v = resp.data.mail_list_external_member_cap ?? MAX_MAIL_LIST_EXTERNAL_MEMBER_CAP;
        if (!cancelled) {
          setCap(v);
          setSaved(v);
        }
      } catch {
        if (!cancelled) feedback.message.error("Failed to load the distribution list setting");
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  const onSave = async () => {
    if (cap === null) return;
    setSaving(true);
    try {
      await apiClient.patch("/admin/settings", { mail_list_external_member_cap: cap });
      setSaved(cap);
      feedback.message.success(
        cap === 0
          ? "Distribution lists can no longer take outside addresses"
          : `A distribution list can now hold up to ${cap} outside addresses`,
      );
    } catch (err) {
      const msg = (err as { response?: { data?: { message?: string } } })?.response?.data?.message;
      feedback.message.error(msg ? `Not saved: ${msg}` : "Failed to update the distribution list setting");
    } finally {
      setSaving(false);
    }
  };

  return (
    <Card title="Distribution lists" style={{ marginBottom: 16 }} loading={loading}>
      <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
        A distribution list can deliver to addresses on other mail servers, a
        Gmail address for example, next to its member mailboxes. Such a list
        sends each copy from its own address, and the mail server sends at most
        20 per message, so it can have at most 20 recipients, member mailboxes
        included. Set the most outside addresses one list may hold, from 0
        (none) to 20. Lowering the limit keeps the addresses lists already
        hold; a list over it can remove addresses but not add new ones.
      </Typography.Paragraph>
      <Space wrap>
        <InputNumber
          aria-label="Outside addresses per list"
          min={0}
          max={MAX_MAIL_LIST_EXTERNAL_MEMBER_CAP}
          precision={0}
          value={cap}
          onChange={(v) => setCap(v)}
        />
        <Typography.Text type="secondary">outside addresses per list</Typography.Text>
        <Button type="primary" onClick={onSave} loading={saving} disabled={cap === null || cap === saved}>
          Save
        </Button>
      </Space>
    </Card>
  );
};
