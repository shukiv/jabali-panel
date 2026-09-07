// RenameDomainButton — GH #1579. Renames an existing web domain in place
// (POST /domains/:id/rename) instead of the create-new-and-move workaround.
//
// Experimental: the backend refuses when the new name already carries mail in
// Stalwart (mail_domain_conflict), when the domain is the panel's own primary,
// or when it has no website. It moves the docroot, tears down the old name's
// nginx vhost + DNS zone, and re-provisions the new name (SSL is reissued by the
// reconciler). Mail is CARRIED to the new name — the Stalwart registry domain is
// renamed in place, so mailboxes, stored messages, and DKIM follow; users just
// reconfigure their mail clients to mail.<newname>. For a WordPress install it
// rewrites the stored site URL to the new name (best-effort, via wp
// search-replace); any install it could not rewrite — and a catch-all it could
// not re-point — is returned in `warnings` and surfaced here. Other apps'
// internal configuration is not changed — the modal notes that.
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
      const resp = await apiClient.post<{ warnings?: string[] }>(
        `/domains/${domain.id}/rename`,
        { name: normalized },
      );
      feedback.message.success(`Renamed to ${normalized}`);
      // Best-effort app-URL rewrites that did not complete (e.g. a WordPress
      // site URL that must be updated by hand) come back as warnings.
      (resp.data?.warnings ?? []).forEach((w) => feedback.message.warning(w));
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
          message="WordPress site URL is updated automatically"
          description="For a WordPress install, the site URL is rewritten to the new name automatically (via wp search-replace). Other apps' internal settings are not changed — update those in the app itself. If the automatic rewrite can't run, you'll see a warning to update it by hand."
        />
        <Typography.Paragraph style={{ marginBottom: 4 }}>
          Renaming <Typography.Text code>{domain.name}</Typography.Text> will:
        </Typography.Paragraph>
        <ul style={{ margin: "0 0 12px", paddingLeft: 18 }}>
          <li>Move the website files to the new domain's document root</li>
          <li>Rebuild the web server (nginx) configuration for the new name</li>
          <li>Reissue the SSL certificate for the new name</li>
          <li>Recreate the managed DNS zone under the new name and remove the old one</li>
          <li>Carry mail to the new name — mailboxes, stored messages, and DKIM move automatically</li>
          <li>Rewrite a WordPress install's site URL to the new name (other apps unchanged)</li>
        </ul>
        <Typography.Paragraph type="secondary" style={{ marginBottom: 4 }}>
          Mail is carried to the new name automatically. Each mailbox keeps its
          messages and password; after the rename, reconfigure mail clients to
          use <Typography.Text code>mail.{normalized || "new-domain.com"}</Typography.Text> for
          IMAP/SMTP. The per-domain mail certificate is reissued separately.
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
          I understand this is experimental. Mail is carried to the new name and
          mail clients must be reconfigured to the new mail host. A WordPress
          site URL is updated automatically; other apps' internal settings are not.
        </Checkbox>
      </Modal>
    </>
  );
}
