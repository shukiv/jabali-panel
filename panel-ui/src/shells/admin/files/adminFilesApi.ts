// adminFilesApi.ts — typed wrappers around /api/v1/admin/files (GH #1184).
// The whole-filesystem admin File Manager. Every call is gated server-side by
// admin auth + the default-off admin_file_manager_enabled setting, and the
// agent enforces the deny-list; this client just shapes the requests.
import { apiClient } from "../../../apiClient";
import type { FileEntry, FileListResponse } from "../../user/files/filesApi";

export type { FileEntry, FileListResponse };

export type AdminFileReadResponse = {
  path: string;
  content: string;
  is_binary: boolean;
  size: number;
  truncated: boolean;
  mime_type?: string;
};

export async function adminFilesList(path: string): Promise<FileListResponse> {
  const r = await apiClient.get<FileListResponse>("/admin/files", { params: { path } });
  return r.data;
}

export async function adminFilesRead(path: string): Promise<AdminFileReadResponse> {
  const r = await apiClient.get<AdminFileReadResponse>("/admin/files/read", { params: { path } });
  return r.data;
}

export async function adminFilesWrite(path: string, content: string): Promise<void> {
  await apiClient.post("/admin/files/write", { path, content });
}

export async function adminFilesDelete(path: string, recursive: boolean): Promise<void> {
  await apiClient.delete("/admin/files", { params: { path, recursive: recursive ? "true" : "false" } });
}

export async function adminFilesMkdir(path: string): Promise<void> {
  await apiClient.post("/admin/files/mkdir", { path });
}

export async function adminFilesRename(path: string, newName: string): Promise<void> {
  await apiClient.post("/admin/files/rename", { path, new_name: newName });
}

export async function adminFilesChmod(path: string, mode: string): Promise<void> {
  await apiClient.post("/admin/files/chmod", { path, mode });
}
