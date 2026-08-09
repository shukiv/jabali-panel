// Shared disable/enable toggle used by both admin and user domain lists.
// The button flips `is_enabled` via PATCH /domains/:id; the backend persists
// it and schedules the reconciler, which re-renders the vhost to serve the
// disabled page (or the tenant's docroot) as appropriate.
import { useState } from "react";
import { PauseCircleOutlined, PlayCircleOutlined } from "@icons";
import { Button } from "antd";
import { feedback } from "../lib/feedback"; // GH #970: themed toasts
import { useQueryClient } from "@tanstack/react-query";

import { apiClient } from "../apiClient";

// Minimal shape — admin and user shells have slightly different Domain
// records but this button only cares about these two fields.
export type DomainToggleTarget = {
  id: string;
  is_enabled: boolean;
};

export const DomainToggleButton = ({ domain }: { domain: DomainToggleTarget }) => {
  const [loading, setLoading] = useState(false);
  const qc = useQueryClient();

  const handleToggle = async () => {
    setLoading(true);
    try {
      await apiClient.patch(`/domains/${domain.id}`, {
        is_enabled: !domain.is_enabled,
      });
      feedback.notification.success({
        message: domain.is_enabled ? "Domain disabled" : "Domain enabled",
      });
      qc.invalidateQueries({ queryKey: ["list", "domains"] });
      qc.invalidateQueries({ queryKey: ["one", "domains", domain.id] });
    } catch (err) {
      feedback.notification.error({
        message: "Failed to toggle",
        description: (err as Error).message,
      });
    } finally {
      setLoading(false);
    }
  };

  return (
    <Button
      type="text"
      icon={domain.is_enabled ? <PauseCircleOutlined /> : <PlayCircleOutlined />}
      onClick={handleToggle}
      loading={loading}
    >
      {domain.is_enabled ? "Disable" : "Enable"}
    </Button>
  );
};
