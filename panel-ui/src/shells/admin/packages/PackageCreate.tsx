// PackageCreate — admin adapter for creating a hosting package.
//
// The entitlement form lives in the shared PackageEditor Module (JAB-331). This
// shell owns only the create mutation, the "Package created" toast, and the
// redirect back to the list.
import { useNavigate } from "react-router";

import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
import { useCreateMutation } from "../../../hooks/useQueries";
import { PackageEditor } from "../../../components/packages/PackageEditor";
import type { PackageWirePayload } from "../../../components/packages/packageFields";

type PackageCreated = { id: string };

export const PackageCreate = () => {
  const navigate = useNavigate();
  const createMutation = useCreateMutation<PackageCreated, PackageWirePayload>({
    resource: "packages",
  });

  const handleSubmit = async (payload: PackageWirePayload) => {
    try {
      await createMutation.mutateAsync(payload);
      feedback.message.success("Package created");
      navigate("/jabali-admin/packages");
    } catch (err: unknown) {
      feedback.message.error(err instanceof Error ? err.message : "Failed to create package");
    }
  };

  return (
    <PackageEditor title="Create package" submitting={createMutation.isPending} onSubmit={handleSubmit} />
  );
};
