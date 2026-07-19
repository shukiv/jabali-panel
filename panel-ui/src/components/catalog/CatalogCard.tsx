// Shared marketplace catalog card (JAB-167). One card design for all three app
// marketplaces (Docker Apps, Python Apps, PHP Applications): icon + name + a
// right-slot meta badge (version / WSGI-ASGI / Database — whatever the type
// uses, same slot + style), a 3-line description, and one consistent filled
// Install button. Type-specific bits (icon source, meta content, install
// handler) come in as props.
import { Button, Card, Space, Typography } from "antd";
import type { ReactNode } from "react";

export interface CatalogCardProps {
  name: string;
  /** Icon as an image URL (Docker/Python) … */
  iconUrl?: string;
  /** … or an arbitrary node (PHP CmsIcon). iconNode wins when both are set. */
  iconNode?: ReactNode;
  /** Right-slot meta under the name: version string, WSGI/ASGI badge, etc. */
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
  return (
    <Card
      size="small"
      style={{ width: 240 }}
      styles={{ body: { display: "flex", flexDirection: "column", gap: 8 } }}
    >
      <Space align="start">
        {iconNode ?? (iconUrl ? <img src={iconUrl} alt="" width={32} height={32} /> : null)}
        <div>
          <Typography.Text strong>{name}</Typography.Text>
          {meta ? (
            <>
              <br />
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                {meta}
              </Typography.Text>
            </>
          ) : null}
        </div>
      </Space>
      <Typography.Paragraph
        type="secondary"
        ellipsis={{ rows: 3 }}
        style={{ fontSize: 12, margin: 0, minHeight: 48 }}
      >
        {description}
      </Typography.Paragraph>
      <Button
        type="primary"
        size="small"
        block
        loading={installing}
        disabled={disabled}
        onClick={onInstall}
      >
        {installLabel}
      </Button>
    </Card>
  );
}
