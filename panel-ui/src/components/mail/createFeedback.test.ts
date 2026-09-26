import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../lib/feedback", () => ({
  feedback: { message: { success: vi.fn(), warning: vi.fn(), error: vi.fn() } },
}));

import { feedback } from "../../lib/feedback";
import { toastCreateResult } from "./createFeedback";

const msg = feedback.message as unknown as {
  success: ReturnType<typeof vi.fn>;
  warning: ReturnType<typeof vi.fn>;
};

describe("toastCreateResult", () => {
  beforeEach(() => vi.clearAllMocks());

  // A share or forwarder the mail server did not take yet must not read as
  // done: the API's warning.detail is shown as a warning.
  it("shows the API warning instead of a success", () => {
    toastCreateResult(
      { warning: { detail: "saved; the mail server did not accept it yet. The panel retries it." } },
      "Share created",
      "Share saved, but not active yet",
    );
    expect(msg.warning).toHaveBeenCalledWith(
      "Share saved, but not active yet: saved; the mail server did not accept it yet. The panel retries it.",
    );
    expect(msg.success).not.toHaveBeenCalled();
  });

  it("shows a success when there is no warning", () => {
    toastCreateResult({}, "Share created", "Share saved, but not active yet");
    expect(msg.success).toHaveBeenCalledWith("Share created");
    expect(msg.warning).not.toHaveBeenCalled();
  });
});
