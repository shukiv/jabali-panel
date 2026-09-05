// DomainDocRootModal — GH #526. The row-menu launcher for the document-root
// editor; the form body now lives in DomainDocRootPanel (GH #1543) and this
// wrapper only owns the Modal chrome, delegating to the panel with footer={null}.
import { Modal } from "antd";

import { DomainDocRootPanel } from "./DomainDocRootPanel";

interface DomainDocRootModalProps {
  domainId: string;
  domainName: string;
  currentDocRoot: string;
  onClose: () => void;
}

export function DomainDocRootModal({ domainId, domainName, currentDocRoot, onClose }: DomainDocRootModalProps) {
  return (
    <Modal title={`Document root — ${domainName}`} open onCancel={onClose} footer={null} destroyOnClose>
      <DomainDocRootPanel
        domainId={domainId}
        domainName={domainName}
        currentDocRoot={currentDocRoot}
        onSaved={onClose}
      />
    </Modal>
  );
}
