// Clone application modal — presents a destination domain dropdown,
// submits the clone request, and invalidates related caches on success.
// Today only WordPress is clonable (descriptor.AgentCloneCmd is set on
// the WordPress descriptor only); future clonable apps reuse the same
// modal and POST /applications/:id/clone path.

import { useTranslation } from "react-i18next";
import { useEffect, useState } from "react";
import {
  Modal,
  Form,
  Select,
  Button,
  message,
  Typography,
} from "antd";
import { useQueryClient } from "@tanstack/react-query";
import { apiClient } from "../../../apiClient";

type Domain = {
  id: string;
  name: string;
};

type Props = {
  open: boolean;
  onClose: () => void;
  onSuccess: () => void;
  installId: string;
};

type ApiError = {
  response?: {
    data?: {
      detail?: string;
      error?: string;
    };
  };
  message?: string;
};

const extractError = (err: ApiError, fallback: string): string => {
  return (
    err.response?.data?.detail ??
    err.response?.data?.error ??
    err.message ??
    fallback
  );
};

export const CloneApplicationModal = ({
  open,
  onClose,
  onSuccess,
  installId,
}: Props) => {
  const { t } = useTranslation();
  const [form] = Form.useForm<{ dest_domain_id: string }>();
  const [submitting, setSubmitting] = useState(false);
  const [domains, setDomains] = useState<Domain[]>([]);
  const [loadingDomains, setLoadingDomains] = useState(false);
  const qc = useQueryClient();

  // Clone also creates a new DB + DB user, so invalidate those lists too.
  const refreshLists = () => {
    qc.invalidateQueries({ queryKey: ["list", "applications"] });
    qc.invalidateQueries({ queryKey: ["list", "databases"] });
    qc.invalidateQueries({ queryKey: ["list", "database-users"] });
    onSuccess();
  };

  // Load domains when modal opens.
  useEffect(() => {
    if (!open) return;
    let alive = true;
    setLoadingDomains(true);
    apiClient
      .get<{ data: Domain[] }>("/domains", { params: { page: 1, page_size: 100 } })
      .then((resp) => {
        if (!alive) return;
        setDomains(resp.data?.data ?? []);
      })
      .catch((err) => {
        if (!alive) return;
        message.error(extractError(err, "Failed to load domains"));
      })
      .finally(() => {
        if (alive) setLoadingDomains(false);
      });
    return () => {
      alive = false;
    };
  }, [open]);

  const reset = () => {
    form.resetFields();
  };

  const handleClose = () => {
    reset();
    onClose();
  };

  const handleSubmit = async () => {
    try {
      await form.validateFields();
    } catch {
      return;
    }
    const vals = form.getFieldsValue();
    setSubmitting(true);
    try {
      await apiClient.post(`/applications/${installId}/clone`, {
        dest_domain_id: vals.dest_domain_id,
      });
      message.success("Cloning started…");
      refreshLists();
      handleClose();
    } catch (err) {
      message.error(extractError(err as ApiError, "Failed to clone application"));
    } finally {
      setSubmitting(false);
    }
  };

  // Show every domain. Clone preserves the source's subdirectory; the
  // backend returns 409 install_exists only if the destination already
  // hosts an install at exactly that subdir. Surfaced via toast.
  const availableDomains = domains;

  return (
    <Modal
      title={t("cloneapplicationmodal.clone_application")}
      open={open}
      onCancel={handleClose}
      maskClosable={!submitting}
      width={500}
      footer={[
        <Button key="cancel" onClick={handleClose} disabled={submitting}>
          Cancel
        </Button>,
        <Button
          key="submit"
          type="primary"
          loading={submitting}
          onClick={handleSubmit}
          disabled={availableDomains.length === 0}
        >
          Clone
        </Button>,
      ]}
      destroyOnClose
    >
      <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
        Files will be copied and the database will be duplicated with a fresh name. siteurl and
        home will be rewritten automatically.
      </Typography.Paragraph>
      <Form
        form={form}
        layout="vertical"
        disabled={submitting}
      >
        <Form.Item
          label={t("cloneapplicationmodal.destination_domain")}
          name="dest_domain_id"
          rules={[{ required: true, message: "Pick a destination domain" }]}
        >
          <Select
            placeholder={t("cloneapplicationmodal.select_a_domain")}
            loading={loadingDomains}
            options={availableDomains.map((d) => ({
              value: d.id,
              label: d.name,
            }))}
            showSearch
            optionFilterProp="label"
          />
        </Form.Item>
      </Form>
    </Modal>
  );
};
