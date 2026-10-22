// Hooks for the Python Application Manager (ADR-0131 / GH #203). Node.js apps
// (ADR-0175) share it: same table, routes and lifecycle, runtime "node".
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { apiClient } from "../../../apiClient";

export type AppRuntime = "python" | "node";

// AppType is wsgi/asgi for a Python app; node (node <script>) or npm (npm run
// <script>) for a Node.js app.
export type AppType = "wsgi" | "asgi" | "node" | "npm";

export type PythonApp = {
  id: string;
  user_id: string;
  domain_id: string;
  name: string;
  runtime: AppRuntime;
  // A Node.js app keeps its Node.js major here ("22").
  python_version: string;
  app_root: string;
  app_type: AppType;
  entrypoint: string;
  base_uri: string;
  // GH #878: nginx serves base_uri+static_url directly from
  // app_root/static_root (Passenger public/ equivalent). Empty = proxy all.
  static_url?: string;
  static_root?: string;
  // GH #878: same split for user-uploaded media (Django MEDIA_ROOT).
  media_url?: string;
  media_root?: string;
  loopback_port?: number;
  status: string;
  last_error?: string;
  created_at: string;
};

export type CreatePythonAppInput = {
  domain_id: string;
  name: string;
  // runtime defaults to python on the server.
  runtime?: AppRuntime;
  python_version?: string;
  // node_version is "22", the only Node.js version (the server's default).
  node_version?: string;
  app_root: string;
  // app_type + entrypoint are derived from the catalog when `framework` is set,
  // so they are optional on a framework install (JAB-164). An npm app's
  // entrypoint (its package.json script) defaults to start.
  app_type?: AppType;
  entrypoint?: string;
  base_uri: string;
  env?: Record<string, string>;
  framework?: string;
  static_url?: string;
  static_root?: string;
  media_url?: string;
  media_root?: string;
};

// Framework is a marketplace catalog entry (JAB-164), from GET
// /python-apps/frameworks.
export type Framework = {
  slug: string;
  name: string;
  version: string;
  description: string;
  tags?: string[];
  icon?: string;
  app_type: "wsgi" | "asgi";
  server: string;
  python_min?: string;
  needs_db?: string;
  docs?: string;
};

// versionsForFramework keeps the installed runtimes a framework can run on:
// those at or above its python_min (GH #2156: sic's Django 6 needs 3.12). The
// API refuses the rest too; this only keeps them out of the picker.
export function versionsForFramework(versions: string[], pythonMin?: string): string[] {
  if (!pythonMin) return versions;
  const minor = (v: string) => Number(v.split(".")[1]);
  return versions.filter((v) => minor(v) >= minor(pythonMin));
}

// The Node.js version Node.js apps run on (ADR-0175: the server's, only).
export const NODE_APP_VERSION = "22";

// The API's entrypoint rules for a Node.js app (panel-api python_apps.go):
// a .js/.mjs/.cjs file under the app directory with no "." or ".." segment,
// or a package.json script name.
export const nodeEntrypointRe =
  /^[A-Za-z0-9_-][A-Za-z0-9._-]*(\/[A-Za-z0-9_-][A-Za-z0-9._-]*)*\.(js|mjs|cjs)$/;
export const npmScriptRe = /^[A-Za-z0-9][A-Za-z0-9:_.-]{0,63}$/;

// appRuntimes lists the runtimes this server has turned on, Python first. The
// create dialog shows a runtime choice only when there are two.
export function appRuntimes(python: boolean, node: boolean): AppRuntime[] {
  const out: AppRuntime[] = [];
  if (python) out.push("python");
  if (node) out.push("node");
  return out;
}

// The create dialog's fields. The request body is built per runtime from the
// fields that runtime has, so a Node.js app never carries a Python field.
export type CreateAppForm = {
  domain_id: string;
  name: string;
  base_uri?: string;
  app_root: string;
  python_version?: string;
  app_type?: AppType;
  entrypoint?: string;
  static_url?: string;
  static_root?: string;
  media_url?: string;
  media_root?: string;
};

export function createAppBody(
  runtime: AppRuntime,
  v: CreateAppForm,
  framework?: string,
): CreatePythonAppInput {
  const body: CreatePythonAppInput = {
    domain_id: v.domain_id,
    name: v.name,
    runtime,
    app_root: v.app_root,
    base_uri: v.base_uri || "/",
  };
  if (runtime === "python" && framework) {
    // Framework install: the server derives app_type + entrypoint and
    // scaffolds the starter.
    return { ...body, python_version: v.python_version, framework };
  }
  const assets = {
    static_url: v.static_url,
    static_root: v.static_root,
    media_url: v.media_url,
    media_root: v.media_root,
  };
  if (runtime === "python") {
    return { ...body, ...assets, python_version: v.python_version, app_type: v.app_type, entrypoint: v.entrypoint };
  }
  const appType = v.app_type === "npm" ? "npm" : "node";
  const entrypoint = v.entrypoint?.trim();
  return {
    ...body,
    ...assets,
    node_version: NODE_APP_VERSION,
    app_type: appType,
    ...(entrypoint ? { entrypoint } : {}),
  };
}

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

// GH #878: set or clear the static + media asset splits on an existing app.
// Both fields of a pair empty clears that split (nginx goes back to proxying
// those paths).
export function useSetPythonAppStatic() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (vars: {
      id: string;
      static_url: string;
      static_root: string;
      media_url: string;
      media_root: string;
    }) => {
      const { data } = await apiClient.put<{ app: PythonApp }>(
        `/python-apps/${vars.id}/static`,
        {
          static_url: vars.static_url,
          static_root: vars.static_root,
          media_url: vars.media_url,
          media_root: vars.media_root,
        },
      );
      return data.app;
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
