// JAB-300: the shared Domain Inventory grid. The admin (`DomainList`) and
// tenant (`UserDomainList`) screens were two ~500-line copies of the same
// table, columns, toggle/delete lifecycle and row-modal wiring, drifted in
// several places. Both are now thin adapters that render their own header
// (title, owner chip / add drawer) above <DomainInventory audience={…} />.
// Audience policy is a discriminated union kept internal to this module — not
// rebuilt as caller-supplied column/callback bags.
import { useState } from "react";
import { useNavigate } from "react-router";
import { Checkbox, Dropdown, Space, Table } from "antd";
import type { ColumnsType } from "antd/es/table";
import { useQueryClient } from "@tanstack/react-query";

import { apiClient } from "../../apiClient";
import { feedback } from "../../lib/feedback"; // GH #970: themed toasts
import { MoreOutlined } from "@icons";
import { sorterToParams } from "../../utils/tableSorter";
import { RowActionButton } from "../RowActionButton";
import { SearchableTableStringQ } from "../SearchableTable";
import { EmptyWithCTA } from "../EmptyWithCTA";
import { useDeleteMutation } from "../../hooks/useQueries";
import { useTableURL } from "../../hooks/useTableURL";
import { useServerCapabilities } from "../../hooks/useServerCapabilities";
import { useTranslation } from "react-i18next";

import { DomainCacheButton } from "../DomainCacheButton";
import { DomainDirectoryPrivacyModal } from "../DomainDirectoryPrivacyModal";
import { DomainNginxOptionsModal } from "../DomainNginxOptionsModal";
import { DomainRedirectsButton } from "../../shells/DomainRedirectsButton";
import { DomainIndexButton } from "../../shells/DomainIndexButton";
import { DomainInfoButton } from "../../shells/DomainInfoButton";
import { DomainSettingsButton, TenantNginxRulesButton } from "../../shells/DomainSettingsButton";
import { DomainDocRootModal } from "./DomainDocRootModal";
import { DomainChownAction } from "../../shells/admin/domains/DomainChownAction";

import { buildDomainDataColumns, type DomainInventoryAudience } from "./domainColumns";
import { buildDomainMenuItems, type DomainModalType } from "./domainActions";
import type { Domain } from "./types";

export type { DomainInventoryAudience } from "./domainColumns";

type ActiveModal = { domainId: string; type: DomainModalType } | null;

export const DomainInventory = ({ audience }: { audience: DomainInventoryAudience }) => {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const qc = useQueryClient();
  const { data: caps } = useServerCapabilities();
  const [activeModal, setActiveModal] = useState<ActiveModal>(null);
  const [togglingId, setTogglingId] = useState<string | null>(null);

  const ownerId = audience.kind === "admin" ? audience.ownerId : undefined;
  const query = useTableURL<Domain>({
    resource: "domains",
    defaultSort: "name",
    defaultOrder: "asc",
    extraParams: ownerId ? { user_id: ownerId } : undefined,
  });
  const deleteMutation = useDeleteMutation({ resource: "domains" });

  // One PATCH path for every row-level flag flip (enable/disable, preview URL,
  // bot challenge). All three now invalidate both the list and the single-row
  // cache — the tenant preview/bot handlers previously invalidated only the
  // list, so an open detail view could show a stale flag.
  const patchDomain = async (
    id: string,
    body: Record<string, unknown>,
    opts: { success: string; errorPrefix: string },
  ) => {
    try {
      await apiClient.patch(`/domains/${id}`, body);
      feedback.message.success(opts.success);
      qc.invalidateQueries({ queryKey: ["list", "domains"] });
      qc.invalidateQueries({ queryKey: ["one", "domains", id] });
    } catch (err) {
      const e = err as { response?: { data?: { detail?: string; error?: string } } };
      feedback.message.error(
        `${opts.errorPrefix}: ${e.response?.data?.detail ?? e.response?.data?.error ?? (err as Error).message}`,
      );
    }
  };

  const handleToggle = async (r: Domain) => {
    setTogglingId(r.id);
    await patchDomain(
      r.id,
      { is_enabled: !r.is_enabled },
      { success: r.is_enabled ? "Domain disabled" : "Domain enabled", errorPrefix: "Failed to toggle" },
    );
    setTogglingId(null);
  };

  const handleTogglePreview = (r: Domain) =>
    patchDomain(
      r.id,
      { temp_url_enabled: !r.temp_url_enabled },
      {
        success: r.temp_url_enabled ? "Preview URL disabled" : "Preview URL enabled — live within a minute",
        errorPrefix: "Failed to toggle preview URL",
      },
    );

  const handleToggleBot = (r: Domain) =>
    patchDomain(
      r.id,
      { bot_challenge_include: !r.bot_challenge_include },
      {
        success: r.bot_challenge_include
          ? "Bot-detection challenge disabled for this site"
          : "Bot-detection challenge enabled — active within a minute if the server is in Selected-domains mode",
        errorPrefix: "Failed to toggle bot challenge",
      },
    );

  const handleDelete = (r: Domain) => {
    // GH #1382: opt-in "also delete the files". Uncontrolled checkbox → closure
    // flag read in onOk (default off). Admin removes the owner's files, so its
    // copy names the owner.
    let deleteFiles = false;
    const filesCopy =
      audience.kind === "admin"
        ? "Also permanently delete the owner's files for this domain (document root). This cannot be undone."
        : "Also permanently delete the domain's files (document root). This cannot be undone.";
    feedback.modal.confirm({
      title: `Delete domain "${r.name}"?`,
      content: (
        <div>
          <p>This removes the domain, its DNS and its web config. This cannot be undone.</p>
          <Checkbox onChange={(e) => { deleteFiles = e.target.checked; }}>{filesCopy}</Checkbox>
        </div>
      ),
      okText: "Delete",
      okButtonProps: { danger: true },
      onOk: async () => {
        await deleteMutation.mutateAsync({
          id: r.id,
          query: deleteFiles ? { delete_files: "true" } : undefined,
        });
      },
    });
  };

  const handleTableChange: React.ComponentProps<typeof Table<Domain>>["onChange"] = (
    pagination,
    _filters,
    sorter,
  ) => {
    const { sort, order } = sorterToParams<Domain>(sorter);
    query.setParams({
      page: pagination.current ?? 1,
      pageSize: pagination.pageSize ?? 20,
      sort,
      order,
    });
  };

  const moreLabel =
    audience.kind === "admin" ? t("domainlist.more_actions") : t("userdomainlist.more_actions");

  const columns: ColumnsType<Domain> = [
    ...buildDomainDataColumns(audience, { t: (k: string) => t(k), query }),
    {
      key: "actions",
      title: audience.kind === "admin" ? t("domainlist.actions") : t("userdomainlist.actions"),
      render: (_: unknown, r: Domain) => (
        <Space>
          {/* GH #833: one Actions entry point on both the admin and tenant lists. */}
          <Dropdown
            trigger={["click"]}
            menu={{
              items: buildDomainMenuItems(r, {
                audience,
                caps,
                togglingId,
                navigate,
                onOpenModal: (domainId, type) => setActiveModal({ domainId, type }),
                onToggle: handleToggle,
                onTogglePreview: handleTogglePreview,
                onToggleBot: handleToggleBot,
                onDelete: handleDelete,
              }),
            }}
          >
            <RowActionButton icon={<MoreOutlined />} aria-label={moreLabel}>
              Actions
            </RowActionButton>
          </Dropdown>
          {activeModal?.domainId === r.id && activeModal.type === "redirects" && (
            <DomainRedirectsButton domain={r} open={true} onClose={() => setActiveModal(null)} />
          )}
          {activeModal?.domainId === r.id && activeModal.type === "index" && (
            <DomainIndexButton domain={r} open={true} onClose={() => setActiveModal(null)} />
          )}
          {activeModal?.domainId === r.id && activeModal.type === "settings" && (
            <DomainSettingsButton domain={r} open={true} onClose={() => setActiveModal(null)} />
          )}
          {activeModal?.domainId === r.id && activeModal.type === "info" && (
            <DomainInfoButton domain={r} open={true} onClose={() => setActiveModal(null)} />
          )}
          {activeModal?.domainId === r.id && activeModal.type === "caching" && (
            <DomainCacheButton
              domainId={r.id}
              domainName={r.name}
              open={true}
              onClose={() => setActiveModal(null)}
            />
          )}
          {activeModal?.domainId === r.id && activeModal.type === "directory-privacy" && (
            <DomainDirectoryPrivacyModal
              open={true}
              domainId={r.id}
              domainName={r.name}
              onClose={() => setActiveModal(null)}
            />
          )}
          {activeModal?.domainId === r.id && activeModal.type === "nginx-options" && (
            <DomainNginxOptionsModal domainId={r.id} onClose={() => setActiveModal(null)} />
          )}
          {activeModal?.domainId === r.id && activeModal.type === "rewrite-rules" && (
            <TenantNginxRulesButton domain={r} open={true} onClose={() => setActiveModal(null)} />
          )}
          {activeModal?.domainId === r.id && activeModal.type === "document-root" && (
            <DomainDocRootModal
              domainId={r.id}
              domainName={r.name}
              currentDocRoot={r.doc_root}
              onClose={() => setActiveModal(null)}
            />
          )}
          {activeModal?.domainId === r.id && activeModal.type === "chown" && (
            <DomainChownAction domain={r} open={true} onClose={() => setActiveModal(null)} />
          )}
        </Space>
      ),
    },
  ];

  return (
    <SearchableTableStringQ<Domain>
      rowKey="id"
      loading={query.isLoading}
      dataSource={query.items}
      columns={columns}
      initialSearch={query.params.q}
      searchPlaceholder="Search by domain name"
      onSearchChange={(q) => query.setParams({ q, page: 1 })}
      pagination={{
        current: query.params.page,
        pageSize: query.params.pageSize,
        total: query.total,
      }}
      onChange={handleTableChange}
      locale={
        audience.kind === "admin"
          ? {
              emptyText: (
                <EmptyWithCTA
                  description={t("domainlist.no_domains_yet")}
                  ctaLabel="Create domain"
                  onCta={() => navigate("create")}
                />
              ),
            }
          : undefined
      }
    />
  );
};
