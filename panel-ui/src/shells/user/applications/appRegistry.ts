// Shared app-catalog registry hook + types. Both the Catalog tab and the
// Install drawer read the SAME GET /applications/registry via TanStack
// Query so the descriptor list is fetched once and cached (M21 convention,
// mirrors the docker-apps page's listCatalog query).
import { useQuery } from "@tanstack/react-query";
import { apiClient } from "../../../apiClient";

// ParamSpec mirrors panel-api/internal/apps.ParamSpec. The closed Type
// set keeps the renderer finite — adding a sixth type means updating
// both this file and the server validator.
export type ParamSpec = {
  type: "string" | "email" | "password" | "enum" | "bool";
  required?: boolean;
  pattern?: string;
  values?: string[];
  // value_labels maps an enum value to its display label ("IL" →
  // "Israel (IL)"). The Select filters on the label, so large enums are
  // searchable by name. Values without an entry render as themselves.
  value_labels?: Record<string, string>;
  default?: string | boolean | number | null;
  description?: string;
};

// AppDescriptor mirrors the JSON GET /applications/registry returns. We
// carry only the fields the UI renders today; new fields can be added
// without breaking older bundles because consumers read keys they know.
export type AppDescriptor = {
  name: string;
  display_name: string;
  description?: string;
  tags?: string[];
  default_subdirectory: string;
  requires_db: boolean;
  email_login?: boolean;
  root_only?: boolean;
  install_notice?: string;
  install_param_schema?: Record<string, ParamSpec>;
};

async function fetchRegistry(): Promise<AppDescriptor[]> {
  const resp = await apiClient.get<{ data: AppDescriptor[] }>(
    "/applications/registry",
  );
  return resp.data?.data ?? [];
}

// useAppRegistry returns the install catalog. `enabled` lets callers gate
// the fetch (the drawer only needs it while open); the Catalog tab passes
// it always-on. staleTime keeps the list cached across tab switches.
export function useAppRegistry(enabled = true) {
  return useQuery({
    queryKey: ["applications-registry"],
    queryFn: fetchRegistry,
    enabled,
    staleTime: 5 * 60 * 1000,
  });
}
