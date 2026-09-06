// DomainRedirectsButton — the row-menu launcher for per-domain and per-page
// redirects (GH #717). The form body now lives in DomainRedirectsPanel
// (GH #1543); this wrapper owns only the Button + Modal chrome and delegates
// with footer={null}.
import { useState } from "react";
import { SwapOutlined } from "@icons";
import { Button, Modal } from "antd";

import {
  DomainRedirectsPanel,
  type DomainRedirectsTarget,
  type PageRedirect,
} from "./DomainRedirectsPanel";

// Re-export the types so existing importers keep resolving them here.
export type { DomainRedirectsTarget, PageRedirect };

export const DomainRedirectsButton = ({
  domain,
  open: controlledOpen,
  onClose,
}: {
  domain: DomainRedirectsTarget;
  open?: boolean;
  onClose?: () => void;
}) => {
  const [isModalOpen, setIsModalOpen] = useState(false);
  const effectiveOpen = controlledOpen ?? isModalOpen;

  const handleCloseModal = () => {
    if (onClose) {
      onClose();
    } else {
      setIsModalOpen(false);
    }
  };

  return (
    <>
      {controlledOpen === undefined && (
        <Button
          type="text"
          icon={<SwapOutlined />}
          onClick={() => setIsModalOpen(true)}
        >
          Redirects
        </Button>
      )}

      <Modal
        title={`Redirects for ${domain.name}`}
        open={effectiveOpen}
        onCancel={handleCloseModal}
        width={720}
        footer={null}
        destroyOnHidden
      >
        <DomainRedirectsPanel domain={domain} onSaved={handleCloseModal} />
      </Modal>
    </>
  );
};
