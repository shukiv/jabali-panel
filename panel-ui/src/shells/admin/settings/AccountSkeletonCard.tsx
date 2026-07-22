// AccountSkeletonCard — GH #465. Admin manager for the account docroot
// skeleton: the tree of starter files copied into every new domain's web
// root on creation. List + delete on the left; "Add file" uploads a file
// (stored base64) at an operator-chosen relative path.
import { useTranslation } from "react-i18next";
import { useState } from "react";
import {
  Button,
  Card,
  Input,
  List,
  Modal,
  Popconfirm,
  Progress,
  Space,
  Typography,
  Upload,
  message,
} from "antd";
import type { UploadFile } from "antd";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { DeleteOutlined, PlusOutlined, UploadOutlined } from "@icons";

import { apiClient } from "../../../apiClient";

type SkeletonEntry = { rel_path: string; size: number };
type SkeletonList = {
  files: SkeletonEntry[];
  total_bytes: number;
  count: number;
  caps: { max_file_bytes: number; max_total_bytes: number; max_files: number };
};

const LIST_KEY = ["admin", "account-skeleton"] as const;

function humanBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(0)} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}

// Read a File as raw base64 (strip the data: prefix FileReader adds).
function fileToBase64(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const r = new FileReader();
    r.onload = () => {
      const s = String(r.result);
      resolve(s.slice(s.indexOf(",") + 1));
    };
    r.onerror = () => reject(r.error);
    r.readAsDataURL(file);
  });
}

export const AccountSkeletonCard = () => {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [adding, setAdding] = useState(false);
  const [relPath, setRelPath] = useState("");
  const [pending, setPending] = useState<UploadFile | null>(null);

  const list = useQuery<SkeletonList>({
    queryKey: LIST_KEY,
    queryFn: async () => (await apiClient.get<SkeletonList>("/admin/settings/account-skeleton")).data,
  });

  const put = useMutation({
    mutationFn: async () => {
      const file = pending?.originFileObj;
      if (!file) throw new Error("pick a file");
      const path = relPath.trim() || file.name;
      const content_base64 = await fileToBase64(file);
      await apiClient.put("/admin/settings/account-skeleton/file", { rel_path: path, content_base64 });
    },
    onSuccess: () => {
      message.success("File added to the skeleton");
      setAdding(false);
      setRelPath("");
      setPending(null);
      qc.invalidateQueries({ queryKey: LIST_KEY });
    },
    onError: (e: unknown) => {
      const detail = (e as { response?: { data?: { detail?: string; error?: string } } })?.response?.data;
      message.error(detail?.detail ?? detail?.error ?? "Upload failed");
    },
  });

  const del = useMutation({
    mutationFn: async (p: string) =>
      apiClient.delete(`/admin/settings/account-skeleton/file`, { params: { path: p } }),
    onSuccess: () => {
      message.success("Removed");
      qc.invalidateQueries({ queryKey: LIST_KEY });
    },
    onError: () => message.error("Delete failed"),
  });

  const data = list.data;
  const pct = data?.caps.max_total_bytes
    ? Math.min(100, Math.round((data.total_bytes / data.caps.max_total_bytes) * 100))
    : 0;

  return (
    <Card
      title={t("accountskeletoncard.account_skeleton")}
      style={{ marginBottom: 16 }}
      extra={
        <Button icon={<PlusOutlined />} onClick={() => setAdding(true)}>
          Add file
        </Button>
      }
    >
      <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
        Starter files copied into every new domain's web root when it's created.
        Use a path like <Typography.Text code>index.html</Typography.Text> or{" "}
        <Typography.Text code>css/style.css</Typography.Text> to place a file in a
        subfolder. Existing files in a docroot are never overwritten.
      </Typography.Paragraph>

      {data && (
        <div style={{ maxWidth: 320, marginBottom: 12 }}>
          <Progress percent={pct} size="small" status={pct >= 95 ? "exception" : "normal"} />
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            {humanBytes(data.total_bytes)} / {humanBytes(data.caps.max_total_bytes)} · {data.count}/
            {data.caps.max_files} files
          </Typography.Text>
        </div>
      )}

      <List
        loading={list.isLoading}
        locale={{ emptyText: "No skeleton files — new accounts get the default page only." }}
        dataSource={data?.files ?? []}
        renderItem={(f) => (
          <List.Item
            actions={[
              <Popconfirm
                key="del"
                title={`Remove ${f.rel_path}?`}
                onConfirm={() => del.mutate(f.rel_path)}
              >
                <Button size="small" danger icon={<DeleteOutlined />} />
              </Popconfirm>,
            ]}
          >
            <Space>
              <Typography.Text code>{f.rel_path}</Typography.Text>
              <Typography.Text type="secondary">{humanBytes(f.size)}</Typography.Text>
            </Space>
          </List.Item>
        )}
      />

      <Modal
        title={t("accountskeletoncard.add_skeleton_file")}
        open={adding}
        onCancel={() => setAdding(false)}
        onOk={() => put.mutate()}
        okText={t("accountskeletoncard.add")}
        confirmLoading={put.isPending}
        okButtonProps={{ disabled: !pending }}
      >
        <Space direction="vertical" style={{ width: "100%" }}>
          <Upload
            maxCount={1}
            beforeUpload={() => false}
            fileList={pending ? [pending] : []}
            onChange={({ fileList }) => setPending(fileList[0] ?? null)}
            onRemove={() => setPending(null)}
          >
            <Button icon={<UploadOutlined />}>Choose file</Button>
          </Upload>
          <Input
            addonBefore={t("accountskeletoncard.path")}
            placeholder={pending?.name ? pending.name : "index.html or css/style.css"}
            value={relPath}
            onChange={(e) => setRelPath(e.target.value)}
          />
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            Relative to the docroot. Leave blank to use the file's own name.
          </Typography.Text>
        </Space>
      </Modal>
    </Card>
  );
};
