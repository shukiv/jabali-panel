// RenameDomainButton — GH #1579. The tenant Web Domain page's trigger for the
// in-place rename: a Button that opens the shared RenameDomainDialog. The dialog
// (and all its flow/validation) lives in RenameDomainDialog so the admin domain
// inventory can open the identical modal from its Actions menu.
import { useState } from "react";
import { Button } from "antd";
import { EditOutlined } from "@icons";
import { RenameDomainDialog } from "./RenameDomainDialog";

interface RenameDomainButtonProps {
  domain: { id: string; name: string };
  // Called after a successful rename so the parent can refetch the (same-id)
  // domain and show the new name.
  onRenamed: () => void;
}

export function RenameDomainButton({ domain, onRenamed }: RenameDomainButtonProps) {
  const [open, setOpen] = useState(false);

  return (
    <>
      <Button icon={<EditOutlined />} onClick={() => setOpen(true)}>
        Rename domain
      </Button>
      <RenameDomainDialog
        domain={domain}
        open={open}
        onClose={() => setOpen(false)}
        onRenamed={onRenamed}
      />
    </>
  );
}
