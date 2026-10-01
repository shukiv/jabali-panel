// GH #1816 / ADR-0170 — domain ownership proof in the UI.
//
// A domain a tenant adds stays "pending" until its owner proves control of
// the name with a TXT record at the domain's current DNS provider. While it is
// pending it is not published: no DNS zone, no mail, no trusted certificate,
// and the public gets no response (the site works through the preview URL
// once that is turned on).
//
// DomainOwnershipPanel is the banner on the tenant Web Domain page (the record
// to add, Verify now, the last result and the expiry) and, with `admin`, the
// ownership block on the admin Edit Domain page (Approve / Revoke).
// OwnershipTag marks an unverified domain in a list.
import { useState } from "react";
import { Alert, Button, Descriptions, Popconfirm, Space, Tag, Tooltip, Typography } from "antd";
import { useQuery, useQueryClient } from "@tanstack/react-query";

import { apiClient } from "../../apiClient";
import { feedback } from "../../lib/feedback";
import { shortDateTime } from "../../utils/datetime";
import type { Domain } from "./types";
import { isOwnershipPending, OWNERSHIP_METHOD_TEXT, OWNERSHIP_RESULT_TEXT, type OwnershipView } from "./ownership";

export const OwnershipTag = ({ status }: { status?: string }) =>
  status !== undefined && status !== "verified" ? (
    <Tooltip title="Not verified yet: offline until its owner proves they control the name.">
      <Tag color="orange">Unverified</Tag>
    </Tooltip>
  ) : null;

const errText = (err: unknown): string => {
  const e = err as { response?: { data?: { detail?: string; error?: string } } };
  return e.response?.data?.detail ?? e.response?.data?.error ?? (err as Error).message;
};

type Props = {
  domain: Pick<
    Domain,
    "id" | "name" | "ownership_status" | "ownership_method" | "is_panel_primary" | "temp_url_enabled" | "temp_url"
  > & {
    managed_by?: string;
  };
  // admin shows the verified state too, with Approve / Revoke.
  admin?: boolean;
};

export const DomainOwnershipPanel = ({ domain, admin = false }: Props) => {
  const qc = useQueryClient();
  const pending = isOwnershipPending(domain);
  const [busy, setBusy] = useState<null | "verify" | "approve" | "revoke">(null);

  const viewQ = useQuery({
    queryKey: ["domain-ownership", domain.id],
    queryFn: async () => (await apiClient.get<OwnershipView>(`/domains/${domain.id}/ownership`)).data,
    enabled: pending,
  });

  const refresh = () => {
    void qc.invalidateQueries({ queryKey: ["domain-ownership", domain.id] });
    void qc.invalidateQueries({ queryKey: ["one", "domains", domain.id] });
    void qc.invalidateQueries({ queryKey: ["list", "domains"] });
  };

  const verify = async () => {
    setBusy("verify");
    try {
      const { data } = await apiClient.post<{ result: string }>(`/domains/${domain.id}/ownership/verify`);
      if (data.result === "verified") {
        feedback.message.success(`${domain.name} is verified. Its DNS, certificate and mail are being set up now.`);
      } else {
        feedback.message.info(OWNERSHIP_RESULT_TEXT[data.result] ?? `Not verified yet (${data.result}).`);
      }
    } catch (err) {
      feedback.message.error(`Check failed: ${errText(err)}`);
    } finally {
      setBusy(null);
      refresh();
    }
  };

  const adminAction = async (action: "approve" | "revoke") => {
    setBusy(action);
    try {
      await apiClient.post(`/admin/domain-ownership/domains/${domain.id}/${action}`);
      feedback.message.success(
        action === "approve"
          ? `${domain.name} approved. It is published within a minute.`
          : `${domain.name} is pending again and goes offline within a minute.`,
      );
    } catch (err) {
      feedback.message.error(`${action === "approve" ? "Approve" : "Revoke"} failed: ${errText(err)}`);
    } finally {
      setBusy(null);
      refresh();
    }
  };

  if (!pending) {
    if (!admin || domain.ownership_status === undefined) return null;
    const canRevoke = !domain.is_panel_primary && domain.managed_by !== "docker_app";
    const method = domain.ownership_method ?? "";
    return (
      <Space wrap size="small" style={{ marginBottom: 16 }}>
        <Typography.Text>Ownership:</Typography.Text>
        <Tag color="green">Verified</Tag>
        {method ? (
          <Typography.Text type="secondary">by {OWNERSHIP_METHOD_TEXT[method] ?? method}</Typography.Text>
        ) : null}
        {canRevoke ? (
          <Popconfirm
            title={`Revoke the verification of ${domain.name}?`}
            description="It goes back to pending: its DNS zone, mail and certificate are taken down and its mailboxes cannot sign in until its owner proves it again. Subdomains verified through it follow."
            okText="Revoke"
            okButtonProps={{ danger: true }}
            onConfirm={() => adminAction("revoke")}
          >
            <Button size="small" danger loading={busy === "revoke"}>
              Revoke verification
            </Button>
          </Popconfirm>
        ) : null}
      </Space>
    );
  }

  const view = viewQ.data;
  const last = view?.last_result ?? "";
  const expires = view?.expires_at ?? null;
  // The preview URL is the one way to reach a pending site, but it is off
  // until someone turns it on (Overview tab), so say which case this is.
  const previewURL = domain.temp_url_enabled && domain.temp_url ? domain.temp_url : null;

  return (
    <Alert
      type="warning"
      showIcon
      style={{ marginBottom: 16 }}
      message={`${domain.name} is not verified yet`}
      description={
        <Space direction="vertical" size="small" style={{ width: "100%" }}>
          <Typography.Paragraph style={{ margin: 0 }}>
            Until {admin ? "its owner proves" : "you prove"} control of this name, it is not published: no DNS
            zone, no mail and no trusted certificate, and visitors get no response.{" "}
            {previewURL ? (
              <>
                The site already works through its preview URL:{" "}
                <Typography.Link href={previewURL} target="_blank" rel="noopener noreferrer">
                  {previewURL.replace(/^https?:\/\//, "")}
                </Typography.Link>
              </>
            ) : admin ? (
              "Its owner can turn on the Preview URL to build the site in the meantime."
            ) : (
              "To build and test the site in the meantime, turn on Preview URL on the Overview tab."
            )}
          </Typography.Paragraph>
          <Typography.Paragraph style={{ margin: 0 }}>
            Add this TXT record at the domain&apos;s current DNS provider (records added in this panel do not
            count while the domain is pending):
          </Typography.Paragraph>
          {view ? (
            <Descriptions column={1} size="small" bordered>
              <Descriptions.Item label="Type">TXT</Descriptions.Item>
              <Descriptions.Item label="Name">
                <Typography.Text code copyable>
                  {view.challenge_name}
                </Typography.Text>
              </Descriptions.Item>
              <Descriptions.Item label="Value">
                <Typography.Text code copyable style={{ wordBreak: "break-all" }}>
                  {view.challenge_value}
                </Typography.Text>
              </Descriptions.Item>
            </Descriptions>
          ) : viewQ.isError ? (
            <Typography.Text type="danger">Could not load the record: {errText(viewQ.error)}</Typography.Text>
          ) : (
            <Typography.Text type="secondary">Loading the record…</Typography.Text>
          )}
          {last ? (
            <Typography.Text>
              Last check{view?.checked_at ? ` (${shortDateTime(view.checked_at)})` : ""}:{" "}
              {OWNERSHIP_RESULT_TEXT[last] ?? last}
            </Typography.Text>
          ) : null}
          <Typography.Text type="secondary">
            The panel checks on its own and puts the domain live once two public resolvers see the record. The
            record can be removed after that.
          </Typography.Text>
          {expires ? (
            <Typography.Text type="secondary">
              If it is not verified by {shortDateTime(expires)}, the domain is removed from the account. Its
              site files are kept.
            </Typography.Text>
          ) : null}
          <Space wrap>
            <Button type="primary" loading={busy === "verify"} onClick={verify}>
              Verify now
            </Button>
            {admin ? (
              <Popconfirm
                title={`Approve ${domain.name} without a DNS proof?`}
                description="Approve only when you know who owns this name. It is published at once."
                okText="Approve"
                onConfirm={() => adminAction("approve")}
              >
                <Button loading={busy === "approve"}>Approve as administrator</Button>
              </Popconfirm>
            ) : null}
          </Space>
        </Space>
      }
    />
  );
};
