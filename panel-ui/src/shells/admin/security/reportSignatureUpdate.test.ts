// reportSignatureUpdate — "Update signatures" used to report success even when
// half of it failed (the YARA pack refresh started a unit that no longer
// existed). Each failed half is named.
import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("../../../lib/feedback", () => ({
  feedback: { message: { success: vi.fn(), warning: vi.fn(), error: vi.fn() } },
}));

import { feedback } from "../../../lib/feedback";
import { reportSignatureUpdate } from "./reportSignatureUpdate";

const msg = feedback.message as unknown as {
  success: ReturnType<typeof vi.fn>;
  warning: ReturnType<typeof vi.fn>;
};

beforeEach(() => vi.clearAllMocks());

describe("reportSignatureUpdate", () => {
  it("reports success when both halves started", () => {
    reportSignatureUpdate({ maldet_ok: true, signature_base_ok: true });
    expect(msg.success).toHaveBeenCalledWith("Signature update started");
    expect(msg.warning).not.toHaveBeenCalled();
  });

  it("names the YARA pack when its refresh did not start", () => {
    reportSignatureUpdate({ maldet_ok: true, signature_base_ok: false });
    expect(msg.warning).toHaveBeenCalledWith("Signature update started, but signature-base failed");
    expect(msg.success).not.toHaveBeenCalled();
  });

  it("names both when both failed, or the result is missing", () => {
    reportSignatureUpdate({ maldet_ok: false, signature_base_ok: false });
    reportSignatureUpdate(undefined);
    expect(msg.warning).toHaveBeenNthCalledWith(1, "Signature update started, but maldet and signature-base failed");
    expect(msg.warning).toHaveBeenNthCalledWith(2, "Signature update started, but maldet and signature-base failed");
  });
});
