// PackageEdit — admin adapter for updating a hosting package.
//
// The entitlement form lives in the shared PackageEditor Module (JAB-331). This
// shell owns only the record query, the update mutation, the "Package updated"
// toast, and the redirect back to the list.
import { useNavigate, useParams } from "react-router";

import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
import { useOneQuery, useUpdateMutation } from "../../../hooks/useQueries";
import { PackageEditor } from "../../../components/packages/PackageEditor";
import type { PackageRecord, PackageWirePayload } from "../../../components/packages/packageFields";

export const PackageEdit = () => {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();

  const { data, isLoading } = useOneQuery<PackageRecord>({ resource: "packages", id });
  const updateMutation = useUpdateMutation<PackageRecord, PackageWirePayload>({
    resource: "packages",
  });

  const handleSubmit = async (payload: PackageWirePayload) => {
    if (!id) return;
    try {
      await updateMutation.mutateAsync({ id, input: payload });
      feedback.message.success("Package updated");
      navigate("/jabali-admin/packages");
    } catch (err: unknown) {
      feedback.message.error(err instanceof Error ? err.message : "Failed to update package");
    }
  };

  return (
    <PackageEditor
      title="Edit package"
      initialValue={data}
      isLoading={isLoading}
      submitting={updateMutation.isPending}
      onSubmit={handleSubmit}
    />
  );
};
