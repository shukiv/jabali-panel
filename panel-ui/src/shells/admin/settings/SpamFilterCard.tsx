// SpamFilterCard — Server Settings → Email: the mail server's spam score
// thresholds (GH #2017). The mail server scores every incoming message; at
// or above the Junk threshold it goes to the Junk folder, at or above the
// reject threshold it is refused at SMTP time, at or above the discard
// threshold it is dropped. Reject and discard can be off (stored as 0). The
// panel owns the values: the reconciler applies them to the mail server
// within a minute, and an update no longer resets them.
import { useEffect, useState } from "react";
import { Alert, Button, Card, InputNumber, Space, Switch, Typography } from "antd";
import { apiClient } from "../../../apiClient";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts

type SpamScores = {
  spam_junk_score: number;
  spam_reject_score: number;
  spam_discard_score: number;
};

type Settings = SpamScores & { mail_enabled?: boolean };

const MAX = 100;

// errorText is the reason the panel gave, if any.
const errorText = (err: unknown, fallback: string): string => {
  const data = (err as { response?: { data?: { detail?: string } } })?.response?.data;
  if (data?.detail) return data.detail;
  return err instanceof Error && err.message ? err.message : fallback;
};

// A threshold that is switched on starts from the stored value, or from a
// value above the Junk threshold when it was off.
const startValue = (stored: number, junk: number, fallback: number): number => {
  if (stored > junk) return stored;
  return Math.min(MAX, Math.max(fallback, junk + 5));
};

export const SpamFilterCard = () => {
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [mailEnabled, setMailEnabled] = useState(true);
  const [junk, setJunk] = useState<number | null>(5);
  const [rejectOn, setRejectOn] = useState(true);
  const [reject, setReject] = useState<number | null>(15);
  const [discardOn, setDiscardOn] = useState(true);
  const [discard, setDiscard] = useState<number | null>(20);

  const apply = (s: SpamScores) => {
    setJunk(s.spam_junk_score);
    setRejectOn(s.spam_reject_score > 0);
    setReject(s.spam_reject_score > 0 ? s.spam_reject_score : startValue(0, s.spam_junk_score, 15));
    setDiscardOn(s.spam_discard_score > 0);
    setDiscard(s.spam_discard_score > 0 ? s.spam_discard_score : startValue(0, s.spam_junk_score, 20));
  };

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const resp = await apiClient.get<Settings>("/admin/settings");
        if (cancelled) return;
        apply(resp.data);
        setMailEnabled(resp.data.mail_enabled !== false);
      } catch {
        if (!cancelled) feedback.message.error("Failed to load the spam filter settings");
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  // The same checks the server runs (mailspam.Scores.Validate), shown before
  // saving.
  let problem: string | null = null;
  if (junk === null || junk <= 0 || junk > MAX) {
    problem = `The Junk threshold must be above 0 and at most ${MAX}.`;
  } else if (rejectOn && (reject === null || reject <= junk || reject > MAX)) {
    problem = `The reject threshold must be above the Junk threshold (${junk}) and at most ${MAX}.`;
  } else if (discardOn && (discard === null || discard <= junk || discard > MAX)) {
    problem = `The discard threshold must be above the Junk threshold (${junk}) and at most ${MAX}.`;
  }
  const discardNeverApplies =
    !problem && rejectOn && discardOn && reject !== null && discard !== null && discard >= reject;

  const save = async () => {
    if (problem || junk === null) return;
    setSaving(true);
    try {
      const resp = await apiClient.patch<Settings>("/admin/settings", {
        spam_junk_score: junk,
        spam_reject_score: rejectOn ? reject : 0,
        spam_discard_score: discardOn ? discard : 0,
      });
      apply(resp.data);
      feedback.message.success("Spam thresholds saved. The mail server uses them within a minute.");
    } catch (e) {
      feedback.message.error(errorText(e, "Could not save the spam thresholds."));
    } finally {
      setSaving(false);
    }
  };

  const toggle = (on: boolean, setOn: (v: boolean) => void, value: number | null, setValue: (v: number) => void, fallback: number) => {
    setOn(on);
    if (on && junk !== null) setValue(startValue(value ?? 0, junk, fallback));
  };

  return (
    <Card title="Spam filter" style={{ marginBottom: 16 }} loading={loading}>
      <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
        The mail server gives every incoming message a spam score. A higher threshold files less mail as
        spam, so fewer real messages end up in Junk and more spam reaches the Inbox. Mail from a sender in
        the mailbox&apos;s contacts, or on its Trusted senders list (Edit mailbox), is not treated as spam when
        the sender&apos;s domain passes SPF or DMARC.
      </Typography.Paragraph>

      {!mailEnabled && (
        <Alert
          type="info"
          showIcon
          style={{ marginBottom: 12 }}
          message="The mail module is off. These thresholds apply once it is turned on."
        />
      )}

      <Space direction="vertical" size="middle" style={{ width: "100%", maxWidth: 520 }}>
        <div>
          <Typography.Text strong>Move to Junk at score</Typography.Text>
          <div>
            <InputNumber
              aria-label="Junk threshold"
              min={0.1}
              max={MAX}
              step={0.5}
              precision={1}
              value={junk}
              onChange={(v) => setJunk(v)}
            />
          </div>
        </div>

        <div>
          <Space>
            <Switch
              aria-label="Reject spam"
              checked={rejectOn}
              onChange={(on) => toggle(on, setRejectOn, reject, setReject, 15)}
            />
            <Typography.Text strong>Reject at score</Typography.Text>
          </Space>
          <div>
            <InputNumber
              aria-label="Reject threshold"
              min={0.1}
              max={MAX}
              step={0.5}
              precision={1}
              value={reject}
              disabled={!rejectOn}
              onChange={(v) => setReject(v)}
            />
          </div>
          <Typography.Text type="secondary">The sending server gets an error, so the sender knows.</Typography.Text>
        </div>

        <div>
          <Space>
            <Switch
              aria-label="Discard spam"
              checked={discardOn}
              onChange={(on) => toggle(on, setDiscardOn, discard, setDiscard, 20)}
            />
            <Typography.Text strong>Discard at score</Typography.Text>
          </Space>
          <div>
            <InputNumber
              aria-label="Discard threshold"
              min={0.1}
              max={MAX}
              step={0.5}
              precision={1}
              value={discard}
              disabled={!discardOn}
              onChange={(v) => setDiscard(v)}
            />
          </div>
          <Typography.Text type="secondary">The message is dropped without telling anyone.</Typography.Text>
        </div>

        {problem && <Alert type="error" showIcon message={problem} />}
        {discardNeverApplies && (
          <Alert
            type="info"
            showIcon
            message="A message at or above the reject threshold is refused first: the mail server rejects it before it could be discarded, so the discard threshold only matters when rejecting is off or set higher."
          />
        )}

        <Button type="primary" loading={saving} disabled={!!problem} onClick={() => void save()}>
          Save
        </Button>
      </Space>
    </Card>
  );
};
