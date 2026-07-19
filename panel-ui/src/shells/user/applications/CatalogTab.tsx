// CatalogTab — the "browse & install" half of the Applications page,
// modelled on the admin Docker-Apps catalog: a masonry of cards, one per
// registered app descriptor (GET /applications/registry). Clicking a card
// opens the Install drawer pre-targeted to that app_type.
import { Alert, Empty, Spin, Tag } from "antd";
import { useMemo, useState } from "react";
import { DatabaseOutlined } from "@icons";

import { CatalogCard, CatalogGrid, CategoryFilter } from "../../../components/catalog";
import { useAppRegistry } from "./appRegistry";
import { CmsIcon } from "./CmsIcon";

type Props = {
  onInstall: (appType: string) => void;
};

export const CatalogTab = ({ onInstall }: Props) => {
  const { data: apps = [], isLoading, isError } = useAppRegistry();
  const [selectedTags, setSelectedTags] = useState<string[]>([]);

  // All tags present in the catalog, sorted, for the filter bar.
  const allTags = useMemo(() => {
    const set = new Set<string>();
    for (const a of apps) for (const t of a.tags ?? []) set.add(t);
    return Array.from(set).sort();
  }, [apps]);

  // OR-match: an app shows if it carries ANY selected tag (best for browsing).
  const visibleApps = useMemo(() => {
    if (selectedTags.length === 0) return apps;
    return apps.filter((a) => (a.tags ?? []).some((t) => selectedTags.includes(t)));
  }, [apps, selectedTags]);

  if (isLoading) {
    return (
      <div style={{ textAlign: "center", padding: 48 }}>
        <Spin />
      </div>
    );
  }

  if (isError) {
    return (
      <Alert
        type="error"
        showIcon
        message="Failed to load the application catalog"
        description="Try reloading the page."
      />
    );
  }

  if (apps.length === 0) {
    return (
      <Empty
        image={Empty.PRESENTED_IMAGE_SIMPLE}
        description="No applications available"
      />
    );
  }

  return (
    <>
      <CategoryFilter tags={allTags} selected={selectedTags} onChange={setSelectedTags} />
      {visibleApps.length === 0 ? (
        <Empty
          image={Empty.PRESENTED_IMAGE_SIMPLE}
          description="No applications match the selected tags"
        />
      ) : (
        <CatalogGrid>
          {visibleApps.map((app) => (
            <CatalogCard
              key={app.name}
              name={app.display_name}
              iconNode={
                <div
                  style={{
                    width: 44,
                    height: 44,
                    borderRadius: 10,
                    background: "rgba(0, 0, 0, 0.03)",
                    display: "flex",
                    alignItems: "center",
                    justifyContent: "center",
                    flexShrink: 0,
                  }}
                >
                  <CmsIcon appType={app.name} size={28} />
                </div>
              }
              meta={
                app.requires_db ? (
                  <Tag icon={<DatabaseOutlined />} style={{ marginInlineEnd: 0 }}>
                    Database
                  </Tag>
                ) : undefined
              }
              description={app.description}
              onInstall={() => onInstall(app.name)}
            />
          ))}
        </CatalogGrid>
      )}
    </>
  );
};
