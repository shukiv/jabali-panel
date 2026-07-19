// Hooks for the Python Application Manager (ADR-0131 / GH #203).
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { apiClient } from "../../../apiClient";

export type PythonApp = {
  id: string;
  user_id: string;
  domain_id: string;
  name: string;
  python_version: string;
  app_root: string;
  app_type: "wsgi" | "asgi";
  entrypoint: string;
  base_uri: string;
  loopback_port?: number;
  status: string;
  last_error?: string;
  created_at: string;
};

export type CreatePythonAppInput = {
  domain_id: string;
  name: string;
  python_version: string;
  app_root: string;
  // app_type + entrypoint are derived from the catalog when `framework` is set,
  // so they are optional on a framework install (JAB-164).
  app_type?: "wsgi" | "asgi";
  entrypoint?: string;
  base_uri: string;
  env?: Record<string, string>;
  framework?: string;
};

// Framework is a marketplace catalog entry (JAB-164), from GET
// /python-apps/frameworks.
export type Framework = {
  slug: string;
  name: string;
  version: string;
  description: string;
  tags?: string[];
  app_type: "wsgi" | "asgi";
  server: string;
  python_min?: string;
  needs_db?: string;
  docs?: string;
};

export function useFrameworks() {
  return useQuery({
    queryKey: ["python-app-frameworks"],
    queryFn: async () => {
      const { data } = await apiClient.get<{ frameworks: Framework[] }>(
        "/python-apps/frameworks",
      );
      return data.frameworks ?? [];
    },
  });
}

const KEY = ["python-apps"];

export function usePythonApps() {
  return useQuery({
    queryKey: KEY,
    queryFn: async () => {
      const { data } = await apiClient.get<{ data: PythonApp[] }>(
        "/python-apps",
      );
      return data.data ?? [];
    },
  });
}

export function useCreatePythonApp() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (input: CreatePythonAppInput) => {
      const { data } = await apiClient.post<{ app: PythonApp }>(
        "/python-apps",
        input,
      );
      return data.app;
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: KEY }),
  });
}

export function useDeletePythonApp() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (id: string) => {
      await apiClient.delete(`/python-apps/${id}`);
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: KEY }),
  });
}

export function useControlPythonApp() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (vars: { id: string; action: string }) => {
      await apiClient.post(`/python-apps/${vars.id}/control`, {
        action: vars.action,
      });
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: KEY }),
  });
}

export async function fetchPythonAppLogs(id: string): Promise<string> {
  const { data } = await apiClient.get<{ logs: string }>(
    `/python-apps/${id}/logs`,
  );
  return data.logs ?? "";
}

export type PythonAppEnvVar = { key: string; value: string };

// fetchPythonAppEnv reads the current env via GET /:id (returns {app, env}).
export async function fetchPythonAppEnv(id: string): Promise<PythonAppEnvVar[]> {
  const { data } = await apiClient.get<{ env?: PythonAppEnvVar[] }>(
    `/python-apps/${id}`,
  );
  return (data.env ?? []).map((e) => ({ key: e.key, value: e.value }));
}

// useUpdatePythonAppEnv replaces the whole env set via the existing PUT
// /python-apps/:id/env contract ({env: {KEY: VALUE}}).
export function useUpdatePythonAppEnv() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (vars: { id: string; env: Record<string, string> }) => {
      await apiClient.put(`/python-apps/${vars.id}/env`, { env: vars.env });
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: KEY }),
  });
}

export type PythonVersions = { versions: string[]; default: string };

// GH #357: the create dialog must only offer interpreters that are actually
// installed on this host, so an app can never be created against a python
// that isn't there (which failed silently at the venv step).
export function usePythonVersions() {
  return useQuery({
    queryKey: ["python-app-versions"],
    queryFn: async () => {
      const { data } = await apiClient.get<PythonVersions>(
        "/python-apps/versions",
      );
      return data;
    },
  });
}
