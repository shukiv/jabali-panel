// RedisKeysDrawer — the tenant's own Redis keys (GH #2165). The tenant's Redis
// user can't SCAN, so the panel lists the keys under the tenant's prefix:
// GET /me/redis-access/keys returns one page and a next_cursor to carry on
// from. Delete goes through POST /me/redis-access/keys/delete, which the panel
// runs with the tenant's own Redis credential.
import { useEffect, useState } from "react";
import { useInfiniteQuery, useQueryClient, type InfiniteData } from "@tanstack/react-query";
import { Alert, Button, Drawer, Grid, Input, Popconfirm, Select, Space, Table, Tag, Tooltip, Typography } from "antd";
import { DeleteOutlined, ReloadOutlined } from "@icons";

import { apiClient } from "../../../apiClient";
import { RowDeleteButton } from "../../../components/RowDeleteButton";
import { Timestamp } from "../../../components/Timestamp";
import { feedback } from "../../../lib/feedback";

const { Text } = Typography;

export interface RedisKeyRow {
  db: number;
  key: string;
  name: string;
  type: string;
  ttl_ms: number;
  expires_at: string | null;
  binary?: boolean;
}

interface RedisKeysPage {
  data: RedisKeyRow[];
  next_cursor: string;
}

// How many keys one call looks for.
const PAGE_LIMIT = 200;
// A page can come back empty when the scan budget ran out before it found any
// of the tenant's keys. Carry on by itself this many times before waiting for
// "Load more".
const MAX_EMPTY_PAGES = 5;

// redisGlobEscape escapes Redis glob characters so a search matches literally
// (the panel-api side does the same for the prefix).
export function redisGlobEscape(s: string): string {
  return s.replace(/[*?[\]\\]/g, (c) => "\\" + c);
}

// searchMatch turns the search box into the listing's match: any key whose
// name contains the text.
export function searchMatch(text: string): string | undefined {
  const t = text.trim();
  return t ? `*${redisGlobEscape(t)}*` : undefined;
}

const rowKey = (r: RedisKeyRow) => `${r.db}:${r.key}`;

interface Props {
  open: boolean;
  onClose: () => void;
}

export function RedisKeysDrawer({ open, onClose }: Props) {
  const screens = Grid.useBreakpoint();
  const isDesktop = screens.lg !== false;
  const qc = useQueryClient();
  const [search, setSearch] = useState("");
  const [db, setDb] = useState<number | "all">("all");
  const [selected, setSelected] = useState<string[]>([]);
  const [deleting, setDeleting] = useState(false);

  const match = searchMatch(search);
  const queryKey = ["redis-keys", match ?? "*", db] as const;
  const query = useInfiniteQuery({
    queryKey,
    enabled: open,
    initialPageParam: "",
    queryFn: async ({ pageParam }) => {
      const params: Record<string, string | number> = { limit: PAGE_LIMIT };
      if (pageParam) params.cursor = pageParam;
      if (match) params.match = match;
      if (db !== "all") params.db = db;
      const resp = await apiClient.get<RedisKeysPage>("/me/redis-access/keys", { params });
      return resp.data;
    },
    getNextPageParam: (last) => last.next_cursor || undefined,
  });

  const pages = query.data?.pages ?? [];
  const rows = pages.flatMap((p) => p.data);

  // Empty pages with a cursor: keep scanning a little on our own.
  let trailingEmpty = 0;
  for (let i = pages.length - 1; i >= 0 && pages[i].data.length === 0; i--) trailingEmpty++;
  const { hasNextPage, isFetching, fetchNextPage } = query;
  useEffect(() => {
    if (open && hasNextPage && !isFetching && trailingEmpty > 0 && trailingEmpty < MAX_EMPTY_PAGES) {
      void fetchNextPage();
    }
  }, [open, hasNextPage, isFetching, trailingEmpty, fetchNextPage]);

  // A filter change starts a new listing.
  useEffect(() => setSelected([]), [match, db]);

  const removeLocally = (gone: Set<string>) => {
    qc.setQueryData<InfiniteData<RedisKeysPage, string>>(queryKey, (old) =>
      old ? { ...old, pages: old.pages.map((p) => ({ ...p, data: p.data.filter((r) => !gone.has(rowKey(r))) })) } : old,
    );
    setSelected((s) => s.filter((k) => !gone.has(k)));
  };

  const deleteKeys = async (targets: RedisKeyRow[]): Promise<number> => {
    const resp = await apiClient.post<{ deleted: number }>("/me/redis-access/keys/delete", {
      keys: targets.map((r) => ({ db: r.db, key: r.key })),
    });
    removeLocally(new Set(targets.map(rowKey)));
    return resp.data.deleted;
  };

  const deleteSelected = async () => {
    const targets = rows.filter((r) => selected.includes(rowKey(r)));
    if (targets.length === 0) return;
    setDeleting(true);
    try {
      const n = await deleteKeys(targets);
      feedback.message.success(`Deleted ${n} key${n === 1 ? "" : "s"}.`);
    } catch {
      feedback.message.error("Could not delete the selected keys.");
    } finally {
      setDeleting(false);
    }
  };

  const refresh = () => {
    setSelected([]);
    void qc.resetQueries({ queryKey });
  };

  return (
    <Drawer
      title="My Redis keys"
      open={open}
      onClose={onClose}
      width={isDesktop ? 800 : undefined}
      placement="right"
      destroyOnClose
    >
      <Space wrap style={{ marginBottom: 16 }}>
        <Input.Search
          allowClear
          placeholder="Search key names"
          aria-label="Search key names"
          onSearch={(v) => setSearch(v)}
          style={{ width: 240 }}
        />
        <Select<number | "all">
          aria-label="Database"
          value={db}
          onChange={setDb}
          style={{ width: 160 }}
          options={[
            { value: "all", label: "All databases" },
            ...Array.from({ length: 16 }, (_, i) => ({ value: i, label: `Database ${i}` })),
          ]}
        />
        <Button icon={<ReloadOutlined />} onClick={refresh}>
          Refresh
        </Button>
        <Popconfirm
          title={`Delete ${selected.length} key${selected.length === 1 ? "" : "s"}?`}
          description="Only your own keys can be deleted."
          okText="Delete"
          okButtonProps={{ danger: true }}
          onConfirm={deleteSelected}
          disabled={selected.length === 0}
        >
          <Button danger icon={<DeleteOutlined />} disabled={selected.length === 0} loading={deleting}>
            Delete selected
          </Button>
        </Popconfirm>
      </Space>

      {query.isError && (
        <Alert type="error" showIcon style={{ marginBottom: 16 }} message="Could not list your Redis keys." />
      )}

      <Table<RedisKeyRow>
        rowKey={rowKey}
        size="small"
        loading={query.isLoading}
        dataSource={rows}
        pagination={false}
        scroll={{ x: "max-content" }}
        rowSelection={{
          selectedRowKeys: selected,
          onChange: (keys) => setSelected(keys as string[]),
          getCheckboxProps: (r) => ({ disabled: !!r.binary }),
        }}
        locale={{ emptyText: hasNextPage ? "No keys found yet" : "No keys" }}
      >
        <Table.Column<RedisKeyRow>
          title="Key"
          dataIndex="name"
          render={(name: string, r) => (
            <Space size={4}>
              <Text code copyable={r.binary ? false : { text: r.key }}>
                {name}
              </Text>
              {r.binary && (
                <Tooltip title="This name isn't valid UTF-8, so it is shown approximately. Delete it with redis-cli.">
                  <Tag>binary</Tag>
                </Tooltip>
              )}
            </Space>
          )}
        />
        <Table.Column<RedisKeyRow> title="DB" dataIndex="db" width={60} />
        <Table.Column<RedisKeyRow> title="Type" dataIndex="type" render={(t: string) => <Tag>{t}</Tag>} />
        <Table.Column<RedisKeyRow>
          title="Expires"
          dataIndex="expires_at"
          render={(v: string | null) => (v ? <Timestamp value={v} seconds /> : <Text type="secondary">Never</Text>)}
        />
        <Table.Column<RedisKeyRow>
          title="Actions"
          key="actions"
          render={(_, r) =>
            r.binary ? null : (
              <RowDeleteButton
                confirmTitle={`Delete ${r.name}?`}
                successMessage="Key deleted"
                onConfirm={async () => {
                  await deleteKeys([r]);
                }}
              />
            )
          }
        />
      </Table>

      <Space style={{ marginTop: 16 }}>
        {hasNextPage ? (
          <>
            <Button onClick={() => void fetchNextPage()} loading={query.isFetchingNextPage}>
              Load more
            </Button>
            <Text type="secondary">
              {rows.length} key{rows.length === 1 ? "" : "s"} so far. The scan isn&apos;t finished, so there may be more.
            </Text>
          </>
        ) : (
          !query.isLoading && (
            <Text type="secondary">
              {rows.length} key{rows.length === 1 ? "" : "s"}.
            </Text>
          )
        )}
      </Space>
    </Drawer>
  );
}
