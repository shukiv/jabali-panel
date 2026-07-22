// NspawnImagesCard — GH #574. Admin GUI for the SSH-sandbox nspawn image
// lifecycle: list sealed images, build a new one (long-running debootstrap run
// as a transient unit, polled for progress), and prune images no user is
// pinned to. Proxies the operator `jabali nspawn build/prune` CLIs via the
// agent; nothing is re-implemented client-side.
import { useTranslation } from "react-i18next";
import { useEffect, useState } from "react";
import {
  Alert,
  App,
  Button,
  Card,
  Form,
  Input,
  List,
  Popconfirm,
  Space,
  Tag,
  Typography,
} from "antd";
import { useMutation, useQuery } from "@tanstack/react-query";
import { apiClient } from "../../../apiClient";
import { JobLogTail } from "../../../components/JobLogTail";

interface NspawnImage {
  name: string;
  version?: string;
  codename?: string;
}
interface BuildStatus {
  status: string;
  log_tail: string;
  exit_code?: number;
}
interface PruneResult {
  output: string;
  exit_code: number;
}

export function NspawnImagesCard() {
  const { t } = useTranslation();
  const { message } = App.useApp();
  const [form] = Form.useForm();
  const [since, setSince] = useState<string | null>(null);
  const [pruneOut, setPruneOut] = useState<string | null>(null);

  const images = useQuery({
    queryKey: ["system", "nspawn-images"],
    queryFn: async () =>
      (await apiClient.get<{ images: NspawnImage[] }>("/system/nspawn-images")).data.images ?? [],
  });

  const status = useQuery({
    queryKey: ["system", "nspawn-build-status", since],
    enabled: since !== null,
    refetchInterval: since !== null ? 3000 : false,
    queryFn: async () =>
      (await apiClient.get<BuildStatus>("/system/nspawn-images/build/status", {
        params: { since },
      })).data,
  });
  const running = status.data?.status === "active" || status.data?.status === "activating";

  // When the build finishes, refresh the image list.
  useEffect(() => {
    if (since !== null && status.data && !running) images.refetch();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [running, status.data]);

  const build = useMutation({
    mutationFn: async (v: {
      codename: string;
      version: string;
      snapshot: string;
      suite?: string;
    }) => (await apiClient.post<{ started_at: string }>("/system/nspawn-images/build", v)).data,
    onSuccess: (d) => {
      setSince(d.started_at);
      message.info("Image build started (debootstrap — this takes a few minutes)");
    },
    onError: (e: unknown) =>
      message.error(e instanceof Error ? e.message : "build failed to start"),
  });

  const prune = useMutation({
    mutationFn: async (apply: boolean) =>
      (await apiClient.post<PruneResult>("/system/nspawn-images/prune", { apply })).data,
    onSuccess: (d, apply) => {
      setPruneOut(d.output);
      if (apply) {
        message.success("Unused images pruned");
        images.refetch();
      }
    },
    onError: (e: unknown) =>
      message.error(e instanceof Error ? e.message : "prune failed"),
  });

  return (
    <Card title={t("nspawnimagescard.ssh_sandbox_images_nspawn")} style={{ marginBottom: 16 }}>
      <Space direction="vertical" size="middle" style={{ width: "100%" }}>
        <Typography.Text type="secondary">
          Sealed, immutable base images for the nspawn SSH sandbox. Building runs
          <Typography.Text code> debootstrap</Typography.Text> against a pinned
          snapshot.debian.org timestamp for deterministic, reproducible rootfs.
        </Typography.Text>

        {/* current images */}
        <List
          size="small"
          bordered
          header={<Typography.Text strong>Installed images</Typography.Text>}
          loading={images.isLoading}
          dataSource={images.data ?? []}
          locale={{ emptyText: "No images built yet" }}
          renderItem={(img) => (
            <List.Item>
              <Tag color="blue">image</Tag>
              <Typography.Text code>{img.name}</Typography.Text>
            </List.Item>
          )}
        />

        {/* build form */}
        <Form
          form={form}
          layout="inline"
          initialValues={{ codename: "debian-13", suite: "trixie" }}
          onFinish={(v) => build.mutate(v)}
        >
          <Form.Item name="codename" label={t("nspawnimagescard.codename")} rules={[{ required: true }]}>
            <Input placeholder="debian-13" style={{ width: 130 }} />
          </Form.Item>
          <Form.Item name="version" label={t("nspawnimagescard.version")} rules={[{ required: true }]}>
            <Input placeholder="v1" style={{ width: 90 }} />
          </Form.Item>
          <Form.Item
            name="snapshot"
            label={t("nspawnimagescard.snapshot")}
            rules={[
              { required: true },
              {
                pattern: /^[0-9]{8}T[0-9]{6}Z$/,
                message: "YYYYMMDDTHHMMSSZ",
              },
            ]}
          >
            <Input placeholder="20260426T000000Z" style={{ width: 175 }} />
          </Form.Item>
          <Form.Item name="suite" label={t("nspawnimagescard.suite")}>
            <Input placeholder="trixie" style={{ width: 100 }} />
          </Form.Item>
          <Form.Item>
            <Button type="primary" htmlType="submit" loading={build.isPending || running}>
              Build image
            </Button>
          </Form.Item>
        </Form>

        {since !== null && status.data && (
          <JobLogTail
            status={status.data.status}
            logTail={status.data.log_tail}
            exitCode={status.data.exit_code}
          />
        )}

        {/* prune */}
        <Space wrap>
          <Button loading={prune.isPending} onClick={() => prune.mutate(false)}>
            Scan for unused images
          </Button>
          <Popconfirm
            title={t("nspawnimagescard.delete_all_unused_images")}
            description={t("nspawnimagescard.removes_every_sealed_image_no_user_and_not_t")}
            okText={t("nspawnimagescard.prune")}
            okButtonProps={{ danger: true }}
            onConfirm={() => prune.mutate(true)}
          >
            <Button danger loading={prune.isPending}>
              Prune unused
            </Button>
          </Popconfirm>
        </Space>
        {pruneOut && (
          <Alert
            type="info"
            message={t("nspawnimagescard.prune_result")}
            description={<pre style={{ whiteSpace: "pre-wrap", margin: 0, fontSize: 12 }}>{pruneOut}</pre>}
          />
        )}
      </Space>
    </Card>
  );
}
