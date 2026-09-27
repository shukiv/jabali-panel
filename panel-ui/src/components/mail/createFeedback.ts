// createFeedback.ts — the toast after a mail create (share, forwarder).
import { feedback } from "../../lib/feedback";

// A create can succeed while the mail server has not taken it yet: the API
// saves the row and returns warning {code, detail} (the panel retries it).
// That is shown as a warning, never as a plain success.
export function toastCreateResult(
  created: { warning?: { detail: string } } | undefined,
  ok: string,
  notLive: string,
): void {
  if (created?.warning) {
    feedback.message.warning(`${notLive}: ${created.warning.detail}`);
    return;
  }
  feedback.message.success(ok);
}
