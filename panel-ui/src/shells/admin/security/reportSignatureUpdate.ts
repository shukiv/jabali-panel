import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
import type { MalwareSignatureUpdate } from "../../../hooks/useSecurityMalware";

// reportSignatureUpdate says which half of "Update signatures" failed: maldet's
// own refresh, or the start of the signature-base YARA pack refresh.
export function reportSignatureUpdate(r: MalwareSignatureUpdate | undefined) {
  const failed: string[] = [];
  if (!r?.maldet_ok) failed.push("maldet");
  if (!r?.signature_base_ok) failed.push("signature-base");
  if (failed.length === 0) {
    feedback.message.success("Signature update started");
  } else {
    feedback.message.warning(`Signature update started, but ${failed.join(" and ")} failed`);
  }
}
