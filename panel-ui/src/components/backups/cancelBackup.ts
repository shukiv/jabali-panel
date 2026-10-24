// GH #2245: Cancel answers once the backup has really stopped, so success
// means cancelled. A backup still stopping after about 20 seconds comes back
// as status "cancelling" (HTTP 202) and turns cancelled on its own once it
// stops. A refusal (it already finished, the agent can't be reached) comes
// back as `detail`.
import { apiClient } from "../../apiClient";
import { extractApiError } from "../../apiErrors";
import { feedback } from "../../lib/feedback";

export async function cancelBackup(path: string): Promise<void> {
  try {
    const resp = await apiClient.post<{ data?: { status?: string } }>(path);
    if (resp?.data?.data?.status === "cancelling") {
      feedback.message.info("The backup is still stopping. It will show as cancelled once it has stopped.");
    } else {
      feedback.message.success("Backup cancelled");
    }
  } catch (err) {
    feedback.message.error(extractApiError(err, "Cancel failed"));
  }
}
