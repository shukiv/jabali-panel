// DomainDocRootPanel — GH #1543 (extracted from DomainDocRootModal, GH #526).
// The document-root form body, rendered both inline (a tab on the tenant Web
// Domain page) and inside the row-menu Modal, which now delegates here. Lets a
// tenant repoint their own domain's document root, confined server-side to the
// domain's own tree (/home/<user>/domains/<domain>/...). Useful for framework
// apps whose real entrypoint is a subdir (e.g. Laravel's public/). Surfaced
// only when the admin has enabled tenant domain options; the backend
// re-validates regardless.
import { useEffect, useState } from "react";
import { Button, Form, Input, Typography } from "antd";
import { feedback } from "../../lib/feedback";
import { useMutation, useQueryClient } from "@tanstack/react-query";

import { apiClient } from "../../apiClient";

// domainTreeBase derives /home/<user>/domains/<domain> from the current docroot
// so the hint shows the exact folder the value must stay inside.
function domainTreeBase(docRoot: string, domainName: string): string {
  const marker = `/domains/${domainName}`;
  const i = docRoot.indexOf(marker);
  return i >= 0 ? docRoot.slice(0, i + marker.length) : `…/domains/${domainName}`;
}

interface DomainDocRootPanelProps {
  domainId: string;
  domainName: string;
  currentDocRoot: string;
  onSaved?: () => void;
}

export function DomainDocRootPanel({
  domainId,
  domainName,
  currentDocRoot,
  onSaved,
}: DomainDocRootPanelProps) {
  const qc = useQueryClient();
  const [form] = Form.useForm<{ doc_root: string }>();
  const [value, setValue] = useState(currentDocRoot);
  const base = domainTreeBase(currentDocRoot, domainName);

  // Re-sync when the source docroot changes underneath (another save, the
  // reconciler) so an inline pane that stays mounted doesn't clobber it.
  useEffect(() => {
    setValue(currentDocRoot);
    form.setFieldsValue({ doc_root: currentDocRoot });
  }, [domainId, currentDocRoot, form]);

  const mut = useMutation({
    mutationFn: async (docRoot: string) => {
      await apiClient.patch(`/domains/${domainId}`, { doc_root: docRoot });
    },
    onSuccess: () => {
      feedback.message.success("Document root updated");
      void qc.invalidateQueries({ queryKey: ["list", "domains"] });
      void qc.invalidateQueries({ queryKey: ["one", "domains", domainId] });
      onSaved?.();
    },
    onError: (err: unknown) => {
      const detail = (err as { response?: { data?: { detail?: string; error?: string } } })?.response?.data;
      feedback.message.error(detail?.detail ?? detail?.error ?? "Failed to update document root");
    },
  });

  return (
    <div>
      <Typography.Paragraph type="secondary" style={{ fontSize: 13 }}>
        Point this domain at a folder inside <Typography.Text code>{base}</Typography.Text>. Handy for apps whose
        entrypoint is a subdirectory (e.g. a framework's <Typography.Text code>public/</Typography.Text>). Leave it
        empty to reset to the default <Typography.Text code>public_html</Typography.Text>.
      </Typography.Paragraph>
      <Form
        form={form}
        layout="vertical"
        onFinish={(v) => void mut.mutateAsync((v.doc_root ?? "").trim())}
      >
        <Form.Item name="doc_root" label="Document root" initialValue={currentDocRoot}>
          <Input
            placeholder={`${base}/public_html`}
            value={value}
            onChange={(e) => setValue(e.target.value)}
            style={{ fontFamily: "monospace" }}
          />
        </Form.Item>
        <Button type="primary" htmlType="submit" loading={mut.isPending}>
          Save
        </Button>
      </Form>
    </div>
  );
}
