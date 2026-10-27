// GitSettingsForm — the repository and deployment settings of a domain's Git
// tab (GH #2277, ADR-0178): connect a repository, or edit the saved one in
// the tab's drawer. One PUT /domains/:id/git saves everything; the server
// reads the repository's branches with the settings first and stores nothing
// if that fails, so the form maps the server's error codes onto its fields.
//
// The saved token never comes back: leaving the field empty keeps it (on the
// same host), "public repository" removes it.
import { useState } from "react";
import { Alert, AutoComplete, Button, Checkbox, Divider, Form, Input, Select } from "antd";
import { useMutation, useQuery } from "@tanstack/react-query";
import type { AxiosError } from "axios";

import { apiClient } from "../../../../apiClient";
import type { Domain } from "../../../../components/domains/types";
import type { GitRemote, GitState } from "./GitTab";

type Values = {
  repo_url: string;
  branch?: string;
  username?: string;
  token?: string;
  clear_token?: boolean;
  web_dir?: string;
  shared_paths?: string[];
  deploy_commands?: string;
};

type SaveError = AxiosError<{ error?: string; detail?: string; remote?: GitRemote }>;

// The field each refusal belongs to; the rest show above the button.
const FIELD_OF: Record<string, keyof Values> = {
  invalid_repo_url: "repo_url",
  invalid_branch: "branch",
  branch_not_found: "branch",
  invalid_username: "username",
  invalid_token: "token",
  token_unreadable: "token",
  invalid_deploy_commands: "deploy_commands",
};

// An https URL with no username or token in it (gitdeploy.ParseRepoURL checks
// the rest).
const repoURLError = (value: string) => {
  let u: URL;
  try {
    u = new URL(value);
  } catch {
    return "Enter the repository's https:// URL.";
  }
  if (u.protocol !== "https:") return "Use the repository's https:// URL; SSH URLs (git@…) are not accepted.";
  if (u.username || u.password) return "Leave the username and token out of the URL: enter them below.";
  return null;
};

export function GitSettingsForm({
  domain,
  state,
  submitLabel,
  onSaved,
}: {
  domain: Domain;
  state: GitState;
  submitLabel: string;
  onSaved: (saved: GitState) => void;
}) {
  const [form] = Form.useForm<Values>();
  const clearToken = Form.useWatch("clear_token", form);
  const [formError, setFormError] = useState<string | null>(null);
  // Branches from a refused save (branch_not_found carries them).
  const [refusedRemote, setRefusedRemote] = useState<GitRemote | undefined>(undefined);

  // The saved repository's branches, for the branch field. Reading them asks
  // the Git host, so only when editing saved settings.
  const branchesQ = useQuery<GitRemote>({
    queryKey: ["git-branches", domain.id],
    queryFn: async () => (await apiClient.get<GitRemote>(`/domains/${domain.id}/git/branches`)).data,
    enabled: state.configured,
    staleTime: 60_000,
    retry: false,
  });
  const branches = (refusedRemote ?? branchesQ.data)?.branches ?? [];

  const save = useMutation({
    mutationFn: async (v: Values) =>
      (
        await apiClient.put<GitState>(`/domains/${domain.id}/git`, {
          repo_url: v.repo_url.trim(),
          branch: (v.branch ?? "").trim(),
          username: (v.username ?? "").trim(),
          token: v.clear_token ? "" : (v.token ?? ""),
          clear_token: !!v.clear_token,
          web_dir: (v.web_dir ?? "").trim(),
          shared_paths: (v.shared_paths ?? []).map((p) => p.trim()).filter(Boolean),
          deploy_commands: v.deploy_commands ?? "",
        })
      ).data,
    onSuccess: (saved) => {
      setFormError(null);
      onSaved(saved);
    },
    onError: (err: SaveError) => {
      const body = err.response?.data;
      const code = body?.error ?? "";
      if (body?.remote) setRefusedRemote(body.remote);
      setFormError(null);
      if (code === "token_required_for_new_host") {
        form.setFields([
          {
            name: "token",
            errors: [
              "The saved token was given for another host. Enter the token for this repository, or tick Public repository.",
            ],
          },
        ]);
        return;
      }
      const field = FIELD_OF[code];
      if (field) {
        form.setFields([{ name: field, errors: [err.message] }]);
        return;
      }
      setFormError(
        code === "git_remote_failed" ? `The server couldn't read the repository: ${err.message}` : err.message,
      );
    },
  });

  return (
    <Form<Values>
      form={form}
      layout="vertical"
      initialValues={{
        repo_url: state.repo_url ?? "",
        branch: state.branch ?? "",
        username: state.username ?? "",
        token: "",
        clear_token: false,
        web_dir: state.web_dir,
        shared_paths: state.shared_paths,
        deploy_commands: state.deploy_commands,
      }}
      onFinish={(v) => save.mutate(v)}
    >
      <Form.Item
        name="repo_url"
        label="Repository URL"
        rules={[
          { required: true, message: "Enter the repository's https:// URL" },
          {
            validator: (_: unknown, value?: string) => {
              const msg = value ? repoURLError(value.trim()) : null;
              return msg ? Promise.reject(new Error(msg)) : Promise.resolve();
            },
          },
        ]}
      >
        <Input placeholder="https://github.com/you/site.git" autoComplete="off" spellCheck={false} />
      </Form.Item>
      <Form.Item
        name="branch"
        label="Branch"
        extra={state.configured ? undefined : "Leave empty to use the repository's default branch."}
      >
        <AutoComplete
          aria-label="Branch"
          options={branches.map((b) => ({ value: b }))}
          placeholder="main"
          filterOption={(input, option) => (option?.value ?? "").toLowerCase().includes(input.toLowerCase())}
        />
      </Form.Item>

      <Divider titlePlacement="start" plain>
        Private repository
      </Divider>
      <Form.Item
        name="token"
        label="Access token"
        extra={
          state.has_token
            ? "Leave empty to keep the saved token. It is never shown again."
            : "A read-only token from your Git host. Leave empty for a public repository."
        }
      >
        <Input.Password autoComplete="new-password" disabled={!!clearToken} spellCheck={false} />
      </Form.Item>
      {state.has_token && (
        <Form.Item name="clear_token" valuePropName="checked">
          <Checkbox>Public repository: remove the saved token</Checkbox>
        </Form.Item>
      )}
      <Form.Item
        name="username"
        label="Username for the token"
        extra="GitHub: your GitHub username. Bitbucket: x-token-auth. GitLab: any name, or a deploy token's own username. Leave empty for a GitHub App token."
      >
        <Input autoComplete="off" spellCheck={false} />
      </Form.Item>

      <Divider titlePlacement="start" plain>
        Deployment
      </Divider>
      <Form.Item
        name="web_dir"
        label="Web directory"
        extra="The folder inside the repository that the site serves: public for Laravel. Leave empty to serve the repository's root. A deployment creates it if the repository doesn't have it."
      >
        <Input placeholder="public" autoComplete="off" spellCheck={false} />
      </Form.Item>
      <Form.Item
        name="shared_paths"
        label="Shared paths"
        extra={`Files and folders kept across deployments in domains/${domain.name}/shared, such as .env or storage. A hidden path like .env can't be inside the web directory.`}
      >
        <Select
          aria-label="Shared paths"
          mode="tags"
          tokenSeparators={[" ", ","]}
          placeholder=".env storage"
          open={false}
          suffixIcon={null}
        />
      </Form.Item>
      <Form.Item
        name="deploy_commands"
        label="Deploy commands"
        extra="Run in the new release before it goes live, one per line, at most 20: php <file> [args], php8.3 <file> [args] or wp [args]. No shell runs them, so pipes, && and variables don't work."
      >
        <Input.TextArea
          rows={4}
          placeholder="php artisan migrate --force"
          spellCheck={false}
          style={{ fontFamily: "ui-monospace, SFMono-Regular, monospace" }}
        />
      </Form.Item>

      {formError && <Alert type="error" showIcon message={formError} style={{ marginBottom: 16 }} />}
      <Button type="primary" htmlType="submit" loading={save.isPending}>
        {save.isPending ? "Checking the repository…" : submitLabel}
      </Button>
    </Form>
  );
}
