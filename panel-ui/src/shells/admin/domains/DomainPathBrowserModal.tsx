// DomainPathBrowserModal — folder picker for the Directory Privacy
// "Path" field. Calls /api/v1/domains/:id/browse to list directories
// under the domain's docroot; user clicks a folder to drill in,
// breadcrumb to navigate up, "Select this folder" returns the path.
import { useTranslation } from "react-i18next";
import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  Breadcrumb,
  Button,
  Empty,
  Modal,
  Space,
  Spin,
  Typography,
} from "antd";
import { FolderOpenOutlined } from "@icons";

import { apiClient } from "../../../apiClient";

type BrowseEntry = { name: string; is_dir: boolean };
type BrowseResp = {
  doc_root: string;
  path: string;
  abs_path: string;
  entries: BrowseEntry[];
};

type Props = {
  domainId: string;
  open: boolean;
  initialPath?: string;
  onCancel: () => void;
  onSelect: (path: string) => void;
};

export const DomainPathBrowserModal = ({
  domainId,
  open,
  initialPath,
  onCancel,
  onSelect,
}: Props) => {
  const { t } = useTranslation();
  const [path, setPath] = useState<string>(initialPath || "/");

  const { data, isLoading, error } = useQuery({
    queryKey: ["domain-browse", domainId, path],
    queryFn: async () => {
      const { data } = await apiClient.get<BrowseResp>(
        `/domains/${domainId}/browse`,
        { params: { path } },
      );
      return data;
    },
    enabled: open,
  });

  const segments = path === "/" ? [] : path.split("/").filter(Boolean);

  const goTo = (idx: number) => {
    if (idx < 0) {
      setPath("/");
      return;
    }
    setPath("/" + segments.slice(0, idx + 1).join("/"));
  };

  const drillInto = (name: string) => {
    const next = path === "/" ? `/${name}` : `${path}/${name}`;
    setPath(next);
  };

  return (
    <Modal
      open={open}
      title={t("domainpathbrowsermodal.pick_a_folder_under_the_docroot")}
      onCancel={onCancel}
      width={620}
      destroyOnClose
      footer={[
        <Button key="cancel" onClick={onCancel}>
          Cancel
        </Button>,
        <Button
          key="select"
          type="primary"
          onClick={() => onSelect(path)}
        >
          Select {path}
        </Button>,
      ]}
    >
      <Space direction="vertical" style={{ width: "100%" }} size="small">
        <Breadcrumb
          items={[
            {
              title: (
                <a onClick={() => goTo(-1)}>
                  <FolderOpenOutlined /> docroot
                </a>
              ),
            },
            ...segments.map((seg, i) => ({
              title:
                i === segments.length - 1 ? (
                  <span>{seg}</span>
                ) : (
                  <a onClick={() => goTo(i)}>{seg}</a>
                ),
            })),
          ]}
        />

        {data?.doc_root && (
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            {data.doc_root}
            {path === "/" ? "" : path}
          </Typography.Text>
        )}

        {isLoading && <Spin />}
        {error && (
          <Typography.Text type="danger">
            Failed to load folder. Owner may lack a Linux account, or
            the docroot may not exist yet.
          </Typography.Text>
        )}
        {!isLoading && data && data.entries.length === 0 && (
          <Empty
            image={Empty.PRESENTED_IMAGE_SIMPLE}
            description={t("domainpathbrowsermodal.no_subfolders_here")}
          />
        )}
        {!isLoading && data && data.entries.length > 0 && (
          <div
            style={{
              maxHeight: 320,
              overflowY: "auto",
              border: "1px solid rgba(255,255,255,0.1)",
              borderRadius: 6,
            }}
          >
            {data.entries.map((e) => (
              <div
                key={e.name}
                onClick={() => drillInto(e.name)}
                style={{
                  padding: "8px 12px",
                  cursor: "pointer",
                  borderBottom: "1px solid rgba(255,255,255,0.05)",
                }}
                onMouseEnter={(ev) =>
                  ((ev.currentTarget as HTMLDivElement).style.background =
                    "rgba(255,255,255,0.04)")
                }
                onMouseLeave={(ev) =>
                  ((ev.currentTarget as HTMLDivElement).style.background =
                    "transparent")
                }
              >
                <FolderOpenOutlined style={{ marginRight: 8 }} />
                {e.name}
              </div>
            ))}
          </div>
        )}
      </Space>
    </Modal>
  );
};
