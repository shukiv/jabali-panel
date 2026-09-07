// RenameDomainButton — GH #1579. Renames an existing web domain in place
// (POST /domains/:id/rename) instead of the create-new-and-move workaround.
//
// Experimental phase 1: the backend refuses when mail is active, when the domain
// is the panel's own primary, or when it has no website. It moves the docroot,
// tears down the old name's nginx vhost + DNS zone, and re-provisions the new
// name (SSL is reissued by the reconciler). It does NOT rewrite an installed
// app's internal configuration (e.g. a WordPress siteurl stored in the app's
// own database) — the modal warns about that and the user updates it in the app.
import { useState } from "react";
import { Alert, Button, Checkbox, Input, Modal, Typography } from "antd";
import { EditOutlined } from "@icons";
import { apiClient } from "../../apiClient";
import { feedback } from "../../lib/feedback";

interface RenameDomainButtonProps {
  domain: { id: string; name: string };
  // Called after a successful rename so the parent can refetch the (same-id)
  // domain and show the new name.
  onRenamed: () => void;
}

// A light client-side FQDN shape check to disable the button early; the server
// runs the authoritative validateDomainName.
const looksLikeFQDN = (s: string): boolean =>
  /^(?:[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,63}$/.test(s);

export function RenameDomainButton({ domain, onRenamed }: RenameDomainButtonProps) {
  const [open, setOpen] = useState(false);
  const [newName, setNewName] = useState("");
  const [ack, setAck] = useState(false);
  const [busy, setBusy] = useState(false);

  const normalized = newName.trim().toLowerCase();
  const canSubmit =
    ack &&
    !busy &&
    looksLikeFQDN(normalized) &&
    normalized !== domain.name.toLowerCase();

  const close = () => {
    setOpen(false);
    setNewName("");
    setAck(false);
  };

  const submit = async () => {
    if (!canSubmit) return;
    setBusy(true);
    try {
      await apiClient.post(`/domains/${domain.id}/rename`, { name: normalized });
      feedback.message.success(`Renamed to ${normalized}`);
      close();
      onRenamed();
    } catch (err) {
      feedback.message.error(
        err instanceof Error ? err.message : "Could not rename the domain",
      );
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      <Button icon={<EditOutlined />} onClick={() => setOpen(true)}>
        Rename domain
      </Button>

      <Modal
        open={open}
        title={`Rename ${domain.name}`}
        okText="Rename domain"
        okButtonProps={{ disabled: !canSubmit, loading: busy }}
        onOk={submit}
        onCancel={close}
        destroyOnClose
      >
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 12 }}
          message="Experimental feature"
          description="Renaming a domain is new. If anything looks wrong afterwards, please file a bug report."
        />
        <Alert
          type="info"
          showIcon
          style={{ marginBottom: 12 }}
          message="Your app's internal settings are not changed"
          description="This does not change WordPress — or any other CMS/app — settings stored inside the app (for example the site URL). After renaming, update the site URL in the app itself."
        />
        <Typography.Paragraph style={{ marginBottom: 4 }}>
          Renaming <Typography.Text code>{domain.name}</Typography.Text> will:
        </Typography.Paragraph>
        <ul style={{ margin: "0 0 12px", paddingLeft: 18 }}>
          <li>Move the website files to the new domain's document root</li>
          <li>Rebuild the web server (nginx) configuration for the new name</li>
          <li>Reissue the SSL certificate for the new name</li>
          <li>Recreate the managed DNS zone under the new name and remove the old one</li>
        </ul>
        <Typography.Paragraph type="secondary" style={{ marginBottom: 4 }}>
          Mail must be off and the domain must have no mailboxes — renaming would
          change every mailbox address. Delete or migrate any mailboxes first.
        </Typography.Paragraph>
        <Input
          autoFocus
          placeholder="new-domain.com"
          value={newName}
          onChange={(e) => setNewName(e.target.value)}
          onPressEnter={submit}
          style={{ marginBottom: 12 }}
        />
        <Checkbox checked={ack} onChange={(e) => setAck(e.target.checked)}>
          I understand this is experimental and does not change my app's internal
          settings.
        </Checkbox>
      </Modal>
    </>
  );
}
