// DomainIndexButton — the row-menu launcher for the Index Manager. The body
// now lives in DomainIndexPanel (GH #1543) so it renders both as a tab on the
// tenant Web Domain page and inside this Modal; this wrapper only owns the
// open/close chrome and delegates the form + save to the panel.
import { useState } from "react";
import { FileTextOutlined } from "@icons";
import { Button, Modal } from "antd";

import { DomainIndexPanel, type DomainIndexTarget } from "./DomainIndexPanel";

export type { IndexPriority, DomainIndexTarget } from "./DomainIndexPanel";

export const DomainIndexButton = ({
  domain,
  open: controlledOpen,
  onClose,
}: {
  domain: DomainIndexTarget;
  open?: boolean;
  onClose?: () => void;
}) => {
  const [internalOpen, setInternalOpen] = useState(false);
  const effectiveOpen = controlledOpen ?? internalOpen;

  const handleClose = () => {
    if (onClose) {
      onClose();
    } else {
      setInternalOpen(false);
    }
  };

  return (
    <>
      {controlledOpen === undefined && (
        <Button type="text" icon={<FileTextOutlined />} onClick={() => setInternalOpen(true)}>
          Index
        </Button>
      )}
      <Modal
        title={`Index Manager for ${domain.name}`}
        open={effectiveOpen}
        onCancel={handleClose}
        width={560}
        footer={null}
        destroyOnClose
      >
        <DomainIndexPanel domain={domain} onSaved={handleClose} />
      </Modal>
    </>
  );
};
