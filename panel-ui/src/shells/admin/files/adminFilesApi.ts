// adminFilesApi.ts — the tenant FilesApi surface bound to /admin/files
// (GH #1184). The admin File Manager renders the SAME FileManagerPage as the
// tenant one, driven by this api object rooted at "/". Every route is gated
// server-side by admin auth + the default-off admin_file_manager_enabled
// setting, and the agent enforces the deny-list.
import { apiClient } from "../../../apiClient";
import {
  filesUpload,
  filesUploadChunked,
  type FilesApi,
  type FileListResponse,
  type FilePreviewResponse,
  type FilesExtractResult,
  type FilesJobStatus,
  type FilesDuResponse,
} from "../../user/files/filesApi";

const BASE = "/admin/files";

export const adminFilesApi: FilesApi = {
  home: async () => ({ path: "/" }),
  list: async (path) =>
    (await apiClient.get<FileListResponse>(BASE, { params: { path } })).data,
  tree: async (path) =>
    (await apiClient.get<FileListResponse>(`${BASE}/tree`, { params: { path } })).data,
  preview: async (path) =>
    (await apiClient.get<FilePreviewResponse>(`${BASE}/preview`, { params: { path } })).data,
  downloadURL: (path) => `/api/v1${BASE}/download?path=${encodeURIComponent(path)}`,
  upload: (dirPath, file, onProgress, opts) =>
    filesUpload(dirPath, file, onProgress, opts, BASE),
  uploadChunked: (dirPath, file, chunkSize, onProgress, opts) =>
    filesUploadChunked(dirPath, file, chunkSize, onProgress, opts, BASE),
  write: async (path, content) => {
    await apiClient.post(`${BASE}/write`, { path, content });
  },
  mkdir: async (path) => {
    await apiClient.post(`${BASE}/mkdir`, { path });
  },
  extract: async (path, dest) =>
    (await apiClient.post<FilesExtractResult>(`${BASE}/extract`, { path, dest })).data,
  extractStart: async (path, dest) =>
    (
      await apiClient.post<{ job_id: string }>(
        `${BASE}/extract`,
        { path, dest },
        { params: { async: 1 } },
      )
    ).data,
  jobStatus: async (jobId) =>
    (
      await apiClient.get<FilesJobStatus>(
        `${BASE}/jobs/${encodeURIComponent(jobId)}`,
      )
    ).data,
  rename: async (path, newName) => {
    await apiClient.post(`${BASE}/rename`, { path, new_name: newName });
  },
  move: async (path, destDir) => {
    await apiClient.post(`${BASE}/move`, { path, dest_dir: destDir });
  },
  chmod: async (path, mode) => {
    await apiClient.post(`${BASE}/chmod`, { path, mode });
  },
  copy: async (path, destDir) => {
    await apiClient.post(`${BASE}/copy`, { path, dest_dir: destDir });
  },
  archive: async (paths) =>
    (await apiClient.post<Blob>(`${BASE}/archive`, { paths }, { responseType: "blob" })).data,
  delete: async (path, recursive = false) => {
    await apiClient.delete(BASE, {
      params: { path, ...(recursive ? { recursive: "true" } : {}) },
    });
  },
  du: async (path) =>
    (await apiClient.get<FilesDuResponse>(`${BASE}/du`, { params: { path } })).data,
};
