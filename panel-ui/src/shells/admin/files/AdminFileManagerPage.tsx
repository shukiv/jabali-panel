// AdminFileManagerPage — GH #1184 whole-filesystem admin File Manager.
//
// Deliberately simpler + separate from the tenant FileManagerPage: browse from
// "/", open+edit text files in CodeEditor, and mkdir / rename / chmod / delete.
// The backend is gated by admin auth + the default-off setting and enforces the
// deny-list, so this page just drives it and surfaces errors.
import { useCallback, useEffect, useMemo, useState } from "react";
import {
  Breadcrumb, Button, Card, Drawer, Input, Modal, Popconfirm, Space, Table, Tag, Tooltip, Typography,
} from "antd";
import {
  FolderOutlined, FileOutlined, ReloadOutlined, FolderAddOutlined, UpOutlined, SaveOutlined,
} from "@icons";
import { feedback } from "../../../lib/feedback";
import { CodeEditor } from "../../../components/CodeEditor/CodeEditor";
import {
  adminFilesList, adminFilesRead, adminFilesWrite, adminFilesDelete,
  adminFilesMkdir, adminFilesRename, adminFilesChmod, type FileEntry,
} from "./adminFilesApi";

const languageByExt: Record<string, string> = {
  conf: "nginx", nginx: "nginx", js: "javascript", ts: "typescript", json: "json",
  php: "php", html: "html", css: "css", sh: "shell", bash: "shell", yml: "yaml",
  yaml: "yaml", toml: "toml", sql: "sql", py: "python", go: "go", md: "markdown",
  ini: "ini", xml: "xml", log: "text", txt: "text",
};

function langFor(name: string): string {
  const ext = name.includes(".") ? name.slice(name.lastIndexOf(".") + 1).toLowerCase() : "";
  return languageByExt[ext] ?? "text";
}

function joinPath(dir: string, name: string): string {
  return dir === "/" ? "/" + name : dir + "/" + name;
}

function parentOf(p: string): string {
  if (p === "/" || p === "") return "/";
  const trimmed = p.replace(/\/+$/, "");
  const idx = trimmed.lastIndexOf("/");
  return idx <= 0 ? "/" : trimmed.slice(0, idx);
}

function errMessage(e: unknown): string {
  if (e && typeof e === "object" && "response" in e) {
    const r = (e as { response?: { data?: { error?: string; detail?: string } } }).response;
    return r?.data?.detail || r?.data?.error || "Request failed";
  }
  return e instanceof Error ? e.message : "Request failed";
}

type PromptMode = null | { kind: "mkdir" } | { kind: "rename"; target: FileEntry } | { kind: "chmod"; target: FileEntry };

export const AdminFileManagerPage = () => {
  const [cwd, setCwd] = useState("/");
  const [entries, setEntries] = useState<FileEntry[]>([]);
  const [loading, setLoading] = useState(false);
  const [editing, setEditing] = useState<{ path: string; content: string; language: string } | null>(null);
  const [saving, setSaving] = useState(false);
  const [prompt, setPrompt] = useState<PromptMode>(null);
  const [promptValue, setPromptValue] = useState("");

  const load = useCallback(async (path: string) => {
    setLoading(true);
    try {
      const r = await adminFilesList(path);
      setEntries(r.entries);
      setCwd(r.path);
    } catch (e) {
      feedback.message.error(errMessage(e));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => { void load("/"); }, [load]);

  const openEntry = async (entry: FileEntry) => {
    const full = joinPath(cwd, entry.name);
    if (entry.is_dir) { void load(full); return; }
    try {
      const r = await adminFilesRead(full);
      if (r.is_binary) { feedback.message.warning("Binary file — not editable here."); return; }
      if (r.truncated) feedback.message.warning("File is large; showing a truncated view. Saving would overwrite with the truncated content — edit via SSH instead.");
      setEditing({ path: full, content: r.content, language: langFor(entry.name) });
    } catch (e) {
      feedback.message.error(errMessage(e));
    }
  };

  const save = async () => {
    if (!editing) return;
    setSaving(true);
    try {
      await adminFilesWrite(editing.path, editing.content);
      feedback.message.success("Saved");
      setEditing(null);
    } catch (e) {
      feedback.message.error(errMessage(e));
    } finally {
      setSaving(false);
    }
  };

  const remove = async (entry: FileEntry) => {
    const full = joinPath(cwd, entry.name);
    try {
      await adminFilesDelete(full, entry.is_dir);
      feedback.message.success("Deleted");
      void load(cwd);
    } catch (e) {
      feedback.message.error(errMessage(e));
    }
  };

  const submitPrompt = async () => {
    if (!prompt) return;
    const v = promptValue.trim();
    try {
      if (prompt.kind === "mkdir") {
        if (!v) return;
        await adminFilesMkdir(joinPath(cwd, v));
      } else if (prompt.kind === "rename") {
        if (!v) return;
        await adminFilesRename(joinPath(cwd, prompt.target.name), v);
      } else if (prompt.kind === "chmod") {
        if (!/^[0-7]{3,4}$/.test(v)) { feedback.message.error("Mode must be octal, e.g. 0644"); return; }
        await adminFilesChmod(joinPath(cwd, prompt.target.name), v);
      }
      feedback.message.success("Done");
      setPrompt(null); setPromptValue("");
      void load(cwd);
    } catch (e) {
      feedback.message.error(errMessage(e));
    }
  };

  const crumbs = useMemo(() => {
    const parts = cwd.split("/").filter(Boolean);
    const items = [{ title: <span style={{ cursor: "pointer" }} onClick={() => void load("/")}><FolderOutlined /> /</span> }];
    let acc = "";
    for (const part of parts) {
      acc += "/" + part;
      const target = acc;
      items.push({ title: <span style={{ cursor: "pointer" }} onClick={() => void load(target)}>{part}</span> });
    }
    return items;
  }, [cwd, load]);

  const columns = [
    {
      title: "Name", dataIndex: "name", key: "name",
      render: (name: string, r: FileEntry) => (
        <span style={{ cursor: "pointer" }} onClick={() => void openEntry(r)}>
          {r.is_dir ? <FolderOutlined style={{ marginRight: 6 }} /> : <FileOutlined style={{ marginRight: 6 }} />}
          {name}{r.is_symlink ? <Tag style={{ marginLeft: 6 }}>link</Tag> : null}
        </span>
      ),
    },
    { title: "Size", dataIndex: "size", key: "size", width: 110, render: (s: number, r: FileEntry) => (r.is_dir ? "—" : `${s}`) },
    { title: "Mode", dataIndex: "mode", key: "mode", width: 110, render: (m: string) => <code>{m}</code> },
    { title: "Modified", dataIndex: "mod_time", key: "mod_time", width: 180 },
    {
      title: "Actions", key: "actions", width: 220,
      render: (_: unknown, r: FileEntry) => (
        <Space size="small">
          <Button size="small" onClick={() => { setPrompt({ kind: "rename", target: r }); setPromptValue(r.name); }}>Rename</Button>
          <Button size="small" onClick={() => { setPrompt({ kind: "chmod", target: r }); setPromptValue(r.mode.replace(/^[dl-]/, "").length === 9 ? "" : ""); }}>Perms</Button>
          <Popconfirm
            title={r.is_dir ? "Delete this folder and everything in it?" : "Delete this file?"}
            okButtonProps={{ danger: true }}
            onConfirm={() => void remove(r)}
          >
            <Button size="small" danger>Delete</Button>
          </Popconfirm>
        </Space>
      ),
    },
  ];

  const promptTitle = prompt?.kind === "mkdir" ? "New folder name"
    : prompt?.kind === "rename" ? "Rename to"
    : prompt?.kind === "chmod" ? "Permissions (octal, e.g. 0644)" : "";

  return (
    <Card
      title={
        <Space>
          <Typography.Text strong>Admin File Manager</Typography.Text>
          <Tag color="red">whole filesystem</Tag>
        </Space>
      }
      extra={
        <Space>
          <Tooltip title="Parent folder"><Button icon={<UpOutlined />} onClick={() => void load(parentOf(cwd))} disabled={cwd === "/"} /></Tooltip>
          <Button icon={<FolderAddOutlined />} onClick={() => { setPrompt({ kind: "mkdir" }); setPromptValue(""); }}>New folder</Button>
          <Button icon={<ReloadOutlined />} onClick={() => void load(cwd)}>Refresh</Button>
        </Space>
      }
    >
      <Breadcrumb style={{ marginBottom: 12 }} items={crumbs} />
      <Table<FileEntry>
        rowKey="name"
        size="small"
        loading={loading}
        columns={columns}
        dataSource={entries}
        pagination={false}
        scroll={{ x: true }}
      />

      <Drawer
        title={editing?.path}
        open={!!editing}
        onClose={() => setEditing(null)}
        width="70%"
        extra={<Button type="primary" icon={<SaveOutlined />} loading={saving} onClick={() => void save()}>Save</Button>}
        destroyOnClose
      >
        {editing ? (
          <CodeEditor value={editing.content} language={editing.language} onChange={(v) => setEditing({ ...editing, content: v })} />
        ) : null}
      </Drawer>

      <Modal
        title={promptTitle}
        open={!!prompt}
        onOk={() => void submitPrompt()}
        onCancel={() => { setPrompt(null); setPromptValue(""); }}
        destroyOnClose
      >
        <Input
          autoFocus
          value={promptValue}
          onChange={(e) => setPromptValue(e.target.value)}
          onPressEnter={() => void submitPrompt()}
          placeholder={prompt?.kind === "chmod" ? "0644" : ""}
        />
      </Modal>
    </Card>
  );
};

export default AdminFileManagerPage;
