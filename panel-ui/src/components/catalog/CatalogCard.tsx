// Shared marketplace catalog card (JAB-167). One card design for all app
// marketplaces (Docker Apps, Python Apps, PHP Applications), modelled on the
// admin Docker-Apps catalog: a full-width masonry card (fills its column), an
// avatar + name + inline meta badge (version / WSGI-ASGI / Database), the full
// description (not truncated), and a single blue "Install" link. Type-specific
// bits (icon, meta content, install handler) come in as props. Pair with
// <CatalogGrid> for the 3-up masonry layout.
import { Button, Card, Space } from "antd";
import type { ReactNode } from "react";

export interface CatalogCardProps {
  name: string;
  /** Icon as an image URL (Docker) — wrapped in the default avatar tile … */
  iconUrl?: string;
  /** … or a pre-styled node (Python's brand tile, PHP's CmsIcon). iconNode wins. */
  iconNode?: ReactNode;
  /** Inline badge next to the name: a version Tag, WSGI/ASGI Tag, Database Tag. */
  meta?: ReactNode;
  description?: string;
  installLabel?: string;
  onInstall: () => void;
  installing?: boolean;
  disabled?: boolean;
}

export function CatalogCard({
  name,
  iconUrl,
  iconNode,
  meta,
  description,
  installLabel = "Install",
  onInstall,
  installing,
  disabled,
}: CatalogCardProps) {
  const avatar =
    iconNode ??
    (iconUrl ? (
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
        <img src={iconUrl} alt="" width={28} height={28} />
      </div>
    ) : null);

  return (
    <div style={{ breakInside: "avoid", marginBottom: 16, display: "inline-block", width: "100%" }}>
      <Card
        hoverable={!disabled}
        onClick={disabled ? undefined : onInstall}
        styles={{ body: { padding: 16 } }}
        actions={[
          <Button
            type="link"
            key="install"
            loading={installing}
            disabled={disabled}
            onClick={(e) => {
              e.stopPropagation();
              onInstall();
            }}
          >
            {installLabel}
          </Button>,
        ]}
      >
        <Card.Meta
          avatar={avatar}
          title={
            <Space size={6} wrap>
              <span>{name}</span>
              {meta}
            </Space>
          }
          description={<div style={{ whiteSpace: "pre-line" }}>{description}</div>}
        />
      </Card>
    </div>
  );
}
