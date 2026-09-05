// DomainNginxOptionsModal — the row-menu launcher for the curated nginx options
// (GH #307). The form body now lives in DomainNginxOptionsPanel (GH #1543); this
// wrapper owns only the Modal chrome and delegates with footer={null}.
import { useTranslation } from "react-i18next";
import { Modal } from "antd";

import { DomainNginxOptionsPanel } from "./DomainNginxOptionsPanel";

export interface DomainNginxOptionsModalProps {
  domainId: string;
  onClose: () => void;
}

export const DomainNginxOptionsModal = ({ domainId, onClose }: DomainNginxOptionsModalProps) => {
  const { t } = useTranslation();
  return (
    <Modal
      open
      title={t("domainnginxoptionsmodal.domain_options")}
      onCancel={onClose}
      footer={null}
      destroyOnClose
    >
      <DomainNginxOptionsPanel domainId={domainId} onSaved={onClose} />
    </Modal>
  );
};
