// AppSecExclusionsPanel — GH #1649. CrowdSec AppSec false-positive triage and
// operator CRS rule exclusions, which used to need a root shell on the box
// (`jabali appsec explain` + `jabali appsec exclusion add`).
//
//   - Recent WAF blocks: the last N AppSec alerts grouped by rule, host and
//     path. The rules that scored are shown apart from the CRS rules that ride
//     along on every block (901340, 949110, 980170), because excluding one of
//     those changes nothing or disables the WAF for the path. Loaded on
//     request only: the agent inspects every alert, which is slow.
//   - Rule exclusions: add (Drawer, optionally prefilled from a block) and
//     remove (Popconfirm). Both apply to the WAF at once; on failure the
//     server undoes the change and says so.
import { useState } from "react";
import type { AxiosError } from "axios";
import { useTranslation } from "react-i18next";
import {
  Alert,
  AutoComplete,
  Button,
  Card,
  Drawer,
  Empty,
  Form,
  Grid,
  Input,
  Select,
  Space,
  Table,
  Tag,
  Tooltip,
  Typography,
} from "antd";
import { PlusOutlined, ReloadOutlined } from "@icons";

import { feedback } from "../../../lib/feedback";
import { RowActionButton } from "../../../components/RowActionButton";
import { RowDeleteButton } from "../../../components/RowDeleteButton";
import { SearchableTableStringQ } from "../../../components/SearchableTable";
import {
  APPSEC_EVENTS_LIMITS,
  useAddAppSecExclusion,
  useAppSecEvents,
  useAppSecExclusions,
  useRemoveAppSecExclusion,
  type AppSecBlockPattern,
  type AppSecExclusion,
  type AppSecInlineBlockGroup,
} from "../../../hooks/useSecurityCrowdsec";
import {
  EMPTY_PREFILL,
  EXCLUSION_HOST_RE,
  EXCLUSION_NOTE_FORBIDDEN_RE,
  EXCLUSION_PATH_FORBIDDEN_RE,
  EXCLUSION_RULE_RE,
  prefillFromPattern,
  type ExclusionFormValues,
  type ExclusionPrefill,
} from "./appsecExclusion";

const fmtTime = (s?: string): string => {
  if (!s) return "—";
  const d = new Date(s);
  return Number.isNaN(d.getTime()) ? s : d.toLocaleString();
};

export const AppSecExclusionsPanel = () => {
  // Each open gets a fresh drawer (and so a fresh form store): a prefill must
  // never be shadowed by the values of the previous open.
  const [drawer, setDrawer] = useState<{ open: boolean; session: number; prefill: ExclusionPrefill }>({
    open: false,
    session: 0,
    prefill: EMPTY_PREFILL,
  });
  const openDrawer = (prefill: ExclusionPrefill) =>
    setDrawer((d) => ({ open: true, session: d.session + 1, prefill }));

  return (
    <Space direction="vertical" size={16} style={{ width: "100%" }}>
      <RecentBlocksCard onExclude={(p) => openDrawer(prefillFromPattern(p))} />
      <ExclusionsCard onAdd={() => openDrawer(EMPTY_PREFILL)} />
      <AddExclusionDrawer
        key={drawer.session}
        open={drawer.open}
        prefill={drawer.prefill}
        onClose={() => setDrawer((d) => ({ ...d, open: false }))}
      />
    </Space>
  );
};

const RecentBlocksCard = ({ onExclude }: { onExclude: (p: AppSecBlockPattern) => void }) => {
  const { t } = useTranslation();
  const [limit, setLimit] = useState<number>(25);
  const [requested, setRequested] = useState(false);
  const events = useAppSecEvents(limit, requested);
  const data = events.data;
  const status = (events.error as AxiosError | null)?.response?.status;

  return (
    <Card
      size="small"
      title={t("appsecexclusionscard.recent_blocks_title")}
      extra={
        <Space size={8} wrap>
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            {t("appsecexclusionscard.alerts_to_inspect")}
          </Typography.Text>
          <Select
            size="small"
            value={limit}
            onChange={setLimit}
            style={{ width: 80 }}
            aria-label={t("appsecexclusionscard.alerts_to_inspect")}
            options={APPSEC_EVENTS_LIMITS.map((n) => ({ value: n, label: String(n) }))}
          />
          <Button
            size="small"
            icon={<ReloadOutlined />}
            loading={events.isFetching}
            onClick={() => (requested ? void events.refetch() : setRequested(true))}
          >
            {requested ? t("appsecexclusionscard.reload") : t("appsecexclusionscard.load")}
          </Button>
        </Space>
      }
    >
      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 12 }}
        message={t("appsecexclusionscard.recent_blocks_help")}
      />

      {events.isError ? (
        <Alert
          type="error"
          showIcon
          style={{ marginBottom: 12 }}
          message={t("appsecexclusionscard.load_failed")}
          description={
            status === 504
              ? t("appsecexclusionscard.timeout_hint")
              : events.error instanceof Error
                ? events.error.message
                : undefined
          }
        />
      ) : null}

      {!requested ? (
        <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t("appsecexclusionscard.not_loaded")} />
      ) : data ? (
        <>
          <Typography.Paragraph type="secondary" style={{ marginBottom: 8 }}>
            {t("appsecexclusionscard.summary", {
              events: data.events_count,
              alerts: data.alerts_scanned,
              patterns: data.patterns.length,
            })}
          </Typography.Paragraph>
          <Table<AppSecBlockPattern>
            size="small"
            rowKey={(p) => `${p.rule_ids.join(",")}|${p.host}|${p.uri}`}
            dataSource={data.patterns}
            loading={events.isFetching}
            pagination={{ pageSize: 10, showSizeChanger: false, hideOnSinglePage: true }}
            scroll={{ x: "max-content" }}
            locale={{
              emptyText: (
                <Empty
                  image={Empty.PRESENTED_IMAGE_SIMPLE}
                  description={t("appsecexclusionscard.no_blocks", { alerts: data.alerts_scanned })}
                />
              ),
            }}
          >
            <Table.Column<AppSecBlockPattern>
              key="count"
              title={t("appsecexclusionscard.col_blocks")}
              render={(_, p) => (
                <Space direction="vertical" size={0}>
                  <Typography.Text strong>{p.count}</Typography.Text>
                  <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                    {t("appsecexclusionscard.distinct_ips", { count: p.distinct_ips })}
                  </Typography.Text>
                </Space>
              )}
            />
            <Table.Column<AppSecBlockPattern>
              key="target"
              title={t("appsecexclusionscard.col_target")}
              render={(_, p) => (
                <Space direction="vertical" size={0} style={{ maxWidth: 360 }}>
                  <Typography.Text code>{p.host || "—"}</Typography.Text>
                  <Typography.Text code ellipsis={{ tooltip: p.uri }} style={{ maxWidth: 360 }}>
                    {p.uri || "—"}
                  </Typography.Text>
                </Space>
              )}
            />
            <Table.Column<AppSecBlockPattern>
              key="detections"
              title={t("appsecexclusionscard.col_scored_by")}
              render={(_, p) =>
                p.detections.length > 0 || p.other.length > 0 ? (
                  <Space size={[4, 4]} wrap>
                    {p.detections.map((id) => (
                      <Tag key={id} color="red">
                        {id}
                      </Tag>
                    ))}
                    {p.other.map((r) => (
                      <Tooltip key={r.id} title={r.note}>
                        <Tag color="purple">{r.id}</Tag>
                      </Tooltip>
                    ))}
                  </Space>
                ) : (
                  <Tooltip title={t("appsecexclusionscard.only_infra_tip")}>
                    <Typography.Text type="secondary">{t("appsecexclusionscard.only_infra")}</Typography.Text>
                  </Tooltip>
                )
              }
            />
            <Table.Column<AppSecBlockPattern>
              key="infra"
              title={t("appsecexclusionscard.col_also_matched")}
              render={(_, p) => (
                <Space size={[4, 4]} wrap>
                  {p.infra.map((r) => (
                    <Tooltip key={r.id} title={r.note}>
                      <Tag>{r.id}</Tag>
                    </Tooltip>
                  ))}
                </Space>
              )}
            />
            <Table.Column<AppSecBlockPattern>
              key="last_at"
              title={t("appsecexclusionscard.col_last_seen")}
              render={(_, p) => fmtTime(p.last_at)}
            />
            <Table.Column<AppSecBlockPattern>
              key="actions"
              title=""
              render={(_, p) => (
                <Tooltip
                  title={
                    p.detections.length > 0
                      ? undefined
                      : p.other.length > 0
                        ? t("appsecexclusionscard.nothing_excludable_tip")
                        : t("appsecexclusionscard.only_infra_tip")
                  }
                >
                  <RowActionButton
                    icon={<PlusOutlined />}
                    disabled={p.detections.length === 0}
                    onClick={() => onExclude(p)}
                  >
                    {t("appsecexclusionscard.exclude")}
                  </RowActionButton>
                </Tooltip>
              )}
            />
          </Table>

          {data.inline_blocks.length > 0 ? (
            <InlineBlocks blocks={data.inline_blocks} total={data.inline_count} />
          ) : null}
        </>
      ) : null}
    </Card>
  );
};

const InlineBlocks = ({ blocks, total }: { blocks: AppSecInlineBlockGroup[]; total: number }) => {
  const { t } = useTranslation();
  return (
    <div style={{ marginTop: 16 }}>
      <Typography.Title level={5}>{t("appsecexclusionscard.inline_title", { count: total })}</Typography.Title>
      <Typography.Paragraph type="secondary">{t("appsecexclusionscard.inline_help")}</Typography.Paragraph>
      <Table<AppSecInlineBlockGroup>
        size="small"
        rowKey="source_ip"
        dataSource={blocks}
        pagination={{ pageSize: 10, showSizeChanger: false, hideOnSinglePage: true }}
        scroll={{ x: "max-content" }}
      >
        <Table.Column<AppSecInlineBlockGroup>
          dataIndex="source_ip"
          key="source_ip"
          title={t("appsecexclusionscard.col_source_ip")}
          render={(v: string) => <Typography.Text code>{v}</Typography.Text>}
        />
        <Table.Column<AppSecInlineBlockGroup> dataIndex="count" key="count" title={t("appsecexclusionscard.col_blocks")} />
        <Table.Column<AppSecInlineBlockGroup>
          dataIndex="last_scores"
          key="last_scores"
          title={t("appsecexclusionscard.col_last_scores")}
        />
        <Table.Column<AppSecInlineBlockGroup>
          dataIndex="last_at"
          key="last_at"
          title={t("appsecexclusionscard.col_last_seen")}
          render={(v: string) => fmtTime(v)}
        />
      </Table>
    </div>
  );
};

const ExclusionsCard = ({ onAdd }: { onAdd: () => void }) => {
  const { t } = useTranslation();
  const list = useAppSecExclusions();
  const remove = useRemoveAppSecExclusion();
  const [query, setQuery] = useState("");
  const q = query.trim().toLowerCase();
  const rows = (list.data ?? []).filter(
    (r) =>
      !q ||
      r.host.toLowerCase().includes(q) ||
      r.uri_prefix.toLowerCase().includes(q) ||
      r.rule_id.includes(q) ||
      r.note.toLowerCase().includes(q),
  );

  return (
    <Card
      size="small"
      title={t("appsecexclusionscard.exclusions_title")}
      extra={
        <Button type="primary" size="small" icon={<PlusOutlined />} onClick={onAdd}>
          {t("appsecexclusionscard.add_exclusion")}
        </Button>
      }
    >
      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 12 }}
        message={t("appsecexclusionscard.exclusions_help")}
      />
      {list.isError ? (
        <Alert
          type="error"
          showIcon
          style={{ marginBottom: 12 }}
          message={list.error instanceof Error ? list.error.message : t("appsecexclusionscard.list_failed")}
        />
      ) : null}
      <SearchableTableStringQ<AppSecExclusion>
        onSearchChange={setQuery}
        searchPlaceholder={t("appsecexclusionscard.search_placeholder")}
        rowKey="id"
        dataSource={rows}
        loading={list.isLoading}
        pagination={{ pageSize: 10, showSizeChanger: false, hideOnSinglePage: true }}
        scroll={{ x: "max-content" }}
        locale={{
          emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t("appsecexclusionscard.no_exclusions")} />,
        }}
      >
        <Table.Column<AppSecExclusion>
          dataIndex="host"
          key="host"
          title={t("appsecexclusionscard.col_host")}
          render={(v: string) => <Typography.Text code>{v}</Typography.Text>}
        />
        <Table.Column<AppSecExclusion>
          dataIndex="uri_prefix"
          key="uri_prefix"
          title={t("appsecexclusionscard.col_path_prefix")}
          render={(v: string) => (
            <Typography.Text code ellipsis={{ tooltip: v }} style={{ maxWidth: 320 }}>
              {v}
            </Typography.Text>
          )}
        />
        <Table.Column<AppSecExclusion>
          dataIndex="rule_id"
          key="rule_id"
          title={t("appsecexclusionscard.col_rule")}
          render={(v: string) => <Tag color="orange">{v}</Tag>}
        />
        <Table.Column<AppSecExclusion>
          key="note"
          title={t("appsecexclusionscard.col_note")}
          render={(_, r) => (
            <Space size={4} wrap>
              {r.managed_install_id ? (
                <Tooltip title={t("appsecexclusionscard.managed_tip", { id: r.managed_install_id })}>
                  <Tag color="blue">{t("appsecexclusionscard.managed_tag")}</Tag>
                </Tooltip>
              ) : null}
              <Typography.Text type="secondary" ellipsis={{ tooltip: r.note }} style={{ maxWidth: 320 }}>
                {r.note || "—"}
              </Typography.Text>
            </Space>
          )}
        />
        <Table.Column<AppSecExclusion>
          dataIndex="created_at"
          key="created_at"
          title={t("appsecexclusionscard.col_added")}
          render={(v: string) => fmtTime(v)}
        />
        <Table.Column<AppSecExclusion>
          key="actions"
          title=""
          render={(_, r) => (
            <RowDeleteButton
              confirmTitle={
                r.managed_install_id
                  ? t("appsecexclusionscard.remove_managed_confirm")
                  : t("appsecexclusionscard.remove_confirm", { rule: r.rule_id, host: r.host, path: r.uri_prefix })
              }
              successMessage={t("appsecexclusionscard.removed")}
              onConfirm={async () => {
                await remove.mutateAsync(r.id);
              }}
            />
          )}
        />
      </SearchableTableStringQ>
    </Card>
  );
};

type AddExclusionDrawerProps = {
  open: boolean;
  prefill: ExclusionPrefill;
  onClose: () => void;
};

const AddExclusionDrawer = ({ open, prefill, onClose }: AddExclusionDrawerProps) => {
  const { t } = useTranslation();
  const [form] = Form.useForm<ExclusionFormValues>();
  const add = useAddAppSecExclusion();
  const [serverError, setServerError] = useState<string | null>(null);
  const screens = Grid.useBreakpoint();
  const isDesktop = screens.lg !== false;
  const fromBlock = prefill.ruleOptions.length > 0;

  const submit = async (v: ExclusionFormValues) => {
    setServerError(null);
    try {
      const res = await add.mutateAsync({
        host: v.host.trim(),
        uri_prefix: v.uri_prefix.trim(),
        rule_id: v.rule_id.trim(),
        note: (v.note ?? "").trim(),
      });
      if (res.apply?.skipped) {
        feedback.message.warning(t("appsecexclusionscard.added_not_live", { reason: res.apply.skipped }));
      } else {
        feedback.message.success(t("appsecexclusionscard.added"));
      }
      onClose();
    } catch (e: unknown) {
      setServerError(e instanceof Error ? e.message : String(e));
    }
  };

  return (
    <Drawer
      title={t("appsecexclusionscard.drawer_title")}
      open={open}
      onClose={onClose}
      width={isDesktop ? 520 : undefined}
      placement="right"
      destroyOnClose
      extra={
        <Space>
          <Button onClick={onClose}>{t("appsecexclusionscard.cancel")}</Button>
          <Button type="primary" loading={add.isPending} onClick={() => form.submit()}>
            {t("appsecexclusionscard.add")}
          </Button>
        </Space>
      }
    >
      {fromBlock ? (
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 16 }}
          message={t("appsecexclusionscard.prefill_warning", { host: prefill.host, path: prefill.uri_prefix })}
        />
      ) : null}
      {serverError ? (
        <Alert type="error" showIcon style={{ marginBottom: 16 }} message={serverError} />
      ) : null}
      <Form<ExclusionFormValues>
        form={form}
        layout="vertical"
        onFinish={submit}
        initialValues={{
          host: prefill.host,
          uri_prefix: prefill.uri_prefix,
          rule_id: prefill.rule_id,
          note: prefill.note,
        }}
      >
        <Form.Item
          name="host"
          label={t("appsecexclusionscard.field_host")}
          extra={t("appsecexclusionscard.field_host_help")}
          rules={[
            { required: true, whitespace: true, message: t("appsecexclusionscard.host_required") },
            {
              validator: (_, v?: string) =>
                !v || EXCLUSION_HOST_RE.test(v.trim())
                  ? Promise.resolve()
                  : Promise.reject(new Error(t("appsecexclusionscard.host_invalid"))),
            },
          ]}
        >
          <Input placeholder="blog.example.com" autoComplete="off" />
        </Form.Item>
        <Form.Item
          name="uri_prefix"
          label={t("appsecexclusionscard.field_path")}
          extra={t("appsecexclusionscard.field_path_help")}
          rules={[
            { required: true, whitespace: true, message: t("appsecexclusionscard.path_required") },
            { max: 512 },
            {
              validator: (_, v?: string) => {
                if (!v) return Promise.resolve();
                if (!v.trim().startsWith("/")) return Promise.reject(new Error(t("appsecexclusionscard.path_slash")));
                if (EXCLUSION_PATH_FORBIDDEN_RE.test(v))
                  return Promise.reject(new Error(t("appsecexclusionscard.path_forbidden")));
                return Promise.resolve();
              },
            },
          ]}
        >
          <Input placeholder="/wp-json/plugin/v1/" autoComplete="off" />
        </Form.Item>
        <Form.Item
          name="rule_id"
          label={t("appsecexclusionscard.field_rule")}
          extra={t("appsecexclusionscard.field_rule_help")}
          rules={[
            { required: true, whitespace: true, message: t("appsecexclusionscard.rule_required") },
            {
              validator: (_, v?: string) =>
                !v || EXCLUSION_RULE_RE.test(v.trim())
                  ? Promise.resolve()
                  : Promise.reject(new Error(t("appsecexclusionscard.rule_invalid"))),
            },
          ]}
        >
          {fromBlock ? (
            <AutoComplete options={prefill.ruleOptions.map((id) => ({ value: id }))} placeholder="942100" />
          ) : (
            <Input placeholder="942100" autoComplete="off" />
          )}
        </Form.Item>
        <Form.Item
          name="note"
          label={t("appsecexclusionscard.field_note")}
          rules={[
            { max: 512 },
            {
              validator: (_, v?: string) =>
                !v || !EXCLUSION_NOTE_FORBIDDEN_RE.test(v)
                  ? Promise.resolve()
                  : Promise.reject(new Error(t("appsecexclusionscard.note_forbidden"))),
            },
          ]}
        >
          <Input autoComplete="off" />
        </Form.Item>
      </Form>
    </Drawer>
  );
};
