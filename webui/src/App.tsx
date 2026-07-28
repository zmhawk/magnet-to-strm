import { useCallback, useEffect, useMemo, useState } from "react";
import {
  Alert,
  App as AntApp,
  Button,
  Card,
  Descriptions,
  Drawer,
  Empty,
  Flex,
  Input,
  Layout,
  Popconfirm,
  Progress,
  Segmented,
  Skeleton,
  Space,
  Statistic,
  Table,
  Tag,
  Tooltip,
  Typography,
} from "antd";
import type { TableProps } from "antd";
import {
  CheckCircleFilled,
  ClockCircleFilled,
  CloudServerOutlined,
  DatabaseOutlined,
  DeleteOutlined,
  LoadingOutlined,
  ReloadOutlined,
  SearchOutlined,
  StopOutlined,
  SyncOutlined,
  WarningFilled,
} from "@ant-design/icons";
import {
  api,
  type Job,
  type JobDetail,
  type JobState,
  type RuntimeStatus,
} from "./api";

const { Header, Content } = Layout;
const { Text, Title, Paragraph } = Typography;

const states: Record<
  JobState,
  { label: string; color: string; icon: React.ReactNode }
> = {
  queued: { label: "等待中", color: "default", icon: <ClockCircleFilled /> },
  running: { label: "进行中", color: "processing", icon: <LoadingOutlined /> },
  succeeded: { label: "已完成", color: "success", icon: <CheckCircleFilled /> },
  failed: { label: "失败", color: "error", icon: <WarningFilled /> },
  canceled: { label: "已取消", color: "default", icon: <WarningFilled /> },
};

function formatTime(value?: string) {
  if (!value) return "—";
  return new Intl.DateTimeFormat("zh-CN", {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  }).format(new Date(value));
}

function formatBytes(value = 0) {
  if (value === 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  const index = Math.min(
    Math.floor(Math.log(value) / Math.log(1024)),
    units.length - 1,
  );
  return `${(value / 1024 ** index).toFixed(index > 1 ? 2 : 0)} ${units[index]}`;
}

function shortHash(value: string) {
  return `${value.slice(0, 8)}…${value.slice(-6)}`;
}

function Dashboard() {
  const { message } = AntApp.useApp();
  const [jobs, setJobs] = useState<Job[]>([]);
  const [runtime, setRuntime] = useState<RuntimeStatus | null>(null);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [query, setQuery] = useState("");
  const [state, setState] = useState<JobState | "all">("all");
  const [selected, setSelected] = useState<JobDetail | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);
  const [operating, setOperating] = useState<string | null>(null);

  const load = useCallback(
    async (quiet = false) => {
      quiet ? setRefreshing(true) : setLoading(true);
      try {
        const [jobData, statusData] = await Promise.all([
          api.jobs(),
          api.status(),
        ]);
        setJobs(jobData.jobs);
        setRuntime(statusData);
      } catch (error) {
        message.error(error instanceof Error ? error.message : "加载任务失败");
      } finally {
        setLoading(false);
        setRefreshing(false);
      }
    },
    [message],
  );

  useEffect(() => {
    void load();
  }, [load]);
  useEffect(() => {
    if (!jobs.some((job) => job.state === "queued" || job.state === "running"))
      return;
    const timer = window.setInterval(() => void load(true), 3000);
    return () => window.clearInterval(timer);
  }, [jobs, load]);

  const counts = useMemo(
    () =>
      jobs.reduce<Record<JobState, number>>(
        (result, job) => ({ ...result, [job.state]: result[job.state] + 1 }),
        { queued: 0, running: 0, succeeded: 0, failed: 0, canceled: 0 },
      ),
    [jobs],
  );

  const filtered = useMemo(() => {
    const needle = query.trim().toLowerCase();
    return jobs.filter((job) => {
      if (state !== "all" && job.state !== state) return false;
      if (!needle) return true;
      return [job.name, job.gid, job.info_hash, job.magnet_uri, job.error].some(
        (value) => value.toLowerCase().includes(needle),
      );
    });
  }, [jobs, query, state]);

  const openDetail = async (job: Job) => {
    setDetailLoading(true);
    setSelected({ job });
    try {
      setSelected(await api.job(job.gid));
    } catch (error) {
      message.error(error instanceof Error ? error.message : "加载详情失败");
    } finally {
      setDetailLoading(false);
    }
  };

  const cancelJob = async (job: Job) => {
    setOperating(job.gid);
    try {
      await api.cancel(job.gid);
      message.success("任务已取消");
      setSelected(null);
      await load(true);
    } catch (error) {
      message.error(error instanceof Error ? error.message : "取消任务失败");
    } finally {
      setOperating(null);
    }
  };

  const deleteJob = async (job: Job) => {
    setOperating(job.gid);
    try {
      await api.delete(job.gid);
      message.success("已删除取消的任务记录");
      setSelected(null);
      await load(true);
    } catch (error) {
      message.error(error instanceof Error ? error.message : "删除任务失败");
    } finally {
      setOperating(null);
    }
  };

  const rebuildSTRMs = async (job: Job) => {
    setOperating(job.gid);
    try {
      const result = await api.rebuildSTRMs(job.gid);
      message.success(`已重建 ${result.rebuilt} 个 STRM 文件`);
    } catch (error) {
      message.error(error instanceof Error ? error.message : "重建 STRM 失败");
    } finally {
      setOperating(null);
    }
  };

  const columns: TableProps<Job>["columns"] = [
    {
      title: "状态",
      dataIndex: "state",
      width: 112,
      render: (value: JobState) => {
        const meta = states[value];
        return (
          <Tag color={meta.color} icon={meta.icon}>
            {meta.label}
          </Tag>
        );
      },
    },
    {
      title: "名称",
      dataIndex: "name",
      ellipsis: true,
      render: (value: string) =>
        value || <Text type="secondary">未命名任务</Text>,
    },
    {
      title: "磁链任务",
      key: "task",
      width: 200,
      render: (_, job) => (
        <Space direction="vertical" size={2}>
          <Button
            type="link"
            className="task-link"
            onClick={() => void openDetail(job)}
          >
            {shortHash(job.info_hash)}
          </Button>
          <Tooltip title={job.gid}>
            <Text type="secondary" className="mono small">
              GID {job.gid}
            </Text>
          </Tooltip>
        </Space>
      ),
    },
    {
      title: "进度",
      dataIndex: "progress",
      width: 170,
      render: (value: number, job) => {
        const percent =
          job.state === "succeeded"
            ? 100
            : Math.max(0, Math.min(100, value || 0));
        return (
          <Progress
            percent={percent}
            size="small"
            status={job.state === "failed" ? "exception" : undefined}
            format={(progress) =>
              job.state === "queued" ? "等待" : `${Math.round(progress || 0)}%`
            }
          />
        );
      },
    },
    {
      title: "创建时间",
      dataIndex: "created_at",
      width: 190,
      responsive: ["md"],
      render: formatTime,
    },
    {
      title: "结果",
      key: "result",
      width: 260,
      responsive: ["lg"],
      render: (_, job) =>
        job.error ? (
          <Tooltip title={job.error}>
            <Text type="danger" ellipsis>
              {job.error}
            </Text>
          </Tooltip>
        ) : (
          <Text type="secondary">
            {job.state === "succeeded" ? "解析和 STRM 写入完成" : "—"}
          </Text>
        ),
    },
    {
      title: "",
      key: "action",
      width: 176,
      align: "right",
      render: (_, job) => (
        <Space size={4}>
          <Button onClick={() => void openDetail(job)}>详情</Button>
          {(job.state === "queued" ||
            job.state === "running" ||
            job.state === "failed") && (
            <Popconfirm
              title="取消任务"
              description="将停止本地处理，并尝试删除对应的 115 离线任务。"
              okText="确认取消"
              cancelText="保留任务"
              onConfirm={() => void cancelJob(job)}
            >
              <Button
                danger
                icon={<StopOutlined />}
                loading={operating === job.gid}
                aria-label="取消任务"
              />
            </Popconfirm>
          )}
          {(job.state === "canceled" || job.state === "failed") && (
            <Popconfirm
              title="删除任务记录"
              description="删除后将不再显示这条失败或已取消的任务。"
              okText="确认删除"
              cancelText="保留记录"
              onConfirm={() => void deleteJob(job)}
            >
              <Button
                danger
                icon={<DeleteOutlined />}
                loading={operating === job.gid}
                aria-label="删除任务"
              />
            </Popconfirm>
          )}
          {job.state === "succeeded" && (
            <Tooltip title="根据本地映射重新生成全部视频 STRM">
              <Button
                icon={<SyncOutlined />}
                loading={operating === job.gid}
                onClick={() => void rebuildSTRMs(job)}
                aria-label="重建 STRM"
              />
            </Tooltip>
          )}
        </Space>
      ),
    },
  ];

  return (
    <Layout className="app-shell">
      <Header className="topbar">
        <div className="brand-mark">
          <CloudServerOutlined />
        </div>
        <div>
          <div className="brand-title">Magnet to STRM</div>
          <div className="brand-subtitle">本地任务控制台</div>
        </div>
        <div className="topbar-spacer" />
        {runtime && (
          <Tag
            color={runtime.p115_enabled ? "success" : "warning"}
            className="mode-tag"
          >
            {runtime.p115_enabled ? "115 已连接" : "本地模式"}
          </Tag>
        )}
      </Header>
      <Content className="content">
        <div className="hero">
          <div>
            <Text className="eyebrow">任务记录</Text>
            <Title level={2}>磁链处理历史</Title>
            <Paragraph type="secondary">
              查看任务状态、错误信息和已写入数据库的解析结果。
            </Paragraph>
          </div>
          <Button
            icon={<ReloadOutlined spin={refreshing} />}
            onClick={() => void load(true)}
            disabled={refreshing}
          >
            刷新
          </Button>
        </div>

        {runtime && !runtime.p115_enabled && (
          <Alert
            className="local-alert"
            type="warning"
            showIcon
            message="当前运行在本地数据库模式"
            description={runtime.message}
          />
        )}

        <div className="stats-grid">
          <Card>
            <Statistic
              title="全部任务"
              value={jobs.length}
              prefix={<DatabaseOutlined />}
            />
          </Card>
          <Card>
            <Statistic
              title="进行中"
              value={counts.running + counts.queued}
              valueStyle={{ color: "#3867e8" }}
            />
          </Card>
          <Card>
            <Statistic
              title="已完成"
              value={counts.succeeded}
              valueStyle={{ color: "#1d8f5a" }}
            />
          </Card>
          <Card>
            <Statistic
              title="失败"
              value={counts.failed}
              valueStyle={{ color: counts.failed ? "#d14343" : undefined }}
            />
          </Card>
        </div>

        <Card className="task-card">
          <Flex
            className="toolbar"
            gap={12}
            justify="space-between"
            align="center"
            wrap
          >
            <Segmented
              value={state}
              onChange={(value) => setState(value as JobState | "all")}
              options={[
                { label: `全部 ${jobs.length}`, value: "all" },
                { label: `等待 ${counts.queued}`, value: "queued" },
                { label: `进行中 ${counts.running}`, value: "running" },
                { label: `完成 ${counts.succeeded}`, value: "succeeded" },
                { label: `失败 ${counts.failed}`, value: "failed" },
                { label: `取消 ${counts.canceled}`, value: "canceled" },
              ]}
            />
            <Input
              className="search"
              allowClear
              prefix={<SearchOutlined />}
              placeholder="搜索 GID、Info Hash 或错误"
              value={query}
              onChange={(event) => setQuery(event.target.value)}
            />
          </Flex>
          <Table<Job>
            rowKey="gid"
            loading={loading}
            columns={columns}
            dataSource={filtered}
            scroll={{ x: 720 }}
            pagination={{
              pageSize: 10,
              showSizeChanger: true,
              showTotal: (total) => `共 ${total} 条`,
            }}
            locale={{
              emptyText: (
                <Empty
                  description={
                    query || state !== "all"
                      ? "没有匹配的任务"
                      : "数据库中还没有任务记录"
                  }
                />
              ),
            }}
          />
        </Card>
      </Content>

      <Drawer
        title="任务详情"
        width={640}
        open={selected !== null}
        onClose={() => setSelected(null)}
        destroyOnHidden
      >
        {selected && detailLoading ? (
          <Skeleton active />
        ) : (
          selected && (
            <Space direction="vertical" size={24} className="detail-stack">
              <Descriptions column={1} bordered size="small">
                <Descriptions.Item label="状态">
                  <Tag color={states[selected.job.state].color}>
                    {states[selected.job.state].label}
                  </Tag>
                </Descriptions.Item>
                <Descriptions.Item label="名称">
                  {selected.job.name || "未命名任务"}
                </Descriptions.Item>
                <Descriptions.Item label="进度">
                  {selected.job.state === "succeeded"
                    ? 100
                    : Math.round(selected.job.progress || 0)}
                  %
                </Descriptions.Item>
                <Descriptions.Item label="GID">
                  <Text copyable className="mono">
                    {selected.job.gid}
                  </Text>
                </Descriptions.Item>
                <Descriptions.Item label="Info Hash">
                  <Text copyable className="mono">
                    {selected.job.info_hash}
                  </Text>
                </Descriptions.Item>
                <Descriptions.Item label="创建时间">
                  {formatTime(selected.job.created_at)}
                </Descriptions.Item>
                <Descriptions.Item label="开始时间">
                  {formatTime(selected.job.started_at)}
                </Descriptions.Item>
                <Descriptions.Item label="完成时间">
                  {formatTime(selected.job.finished_at)}
                </Descriptions.Item>
              </Descriptions>
              {selected.job.error && (
                <Alert
                  type="error"
                  showIcon
                  message="任务错误"
                  description={selected.job.error}
                />
              )}
              <div>
                <Text strong>磁力链接</Text>
                <Paragraph copyable className="code-block">
                  {selected.job.magnet_uri}
                </Paragraph>
              </div>
              {selected.result ? (
                <div>
                  <Flex justify="space-between" align="baseline">
                    <Title level={5}>解析结果</Title>
                    <Text type="secondary">
                      {(selected.result.files ?? []).length} 个文件 ·{" "}
                      {formatBytes(selected.result.total_bytes)}
                    </Text>
                  </Flex>
                  <Descriptions column={1} size="small">
                    <Descriptions.Item label="名称">
                      {selected.result.name || "—"}
                    </Descriptions.Item>
                    <Descriptions.Item label="115 结果 ID">
                      {selected.result.result_id || "—"}
                    </Descriptions.Item>
                    <Descriptions.Item label="扫描时间">
                      {formatTime(selected.result.scanned_at)}
                    </Descriptions.Item>
                  </Descriptions>
                  <Table
                    className="file-table"
                    size="small"
                    rowKey="relative_path"
                    pagination={{ pageSize: 8, hideOnSinglePage: true }}
                    dataSource={selected.result.files ?? []}
                    columns={[
                      {
                        title: "文件",
                        dataIndex: "relative_path",
                        ellipsis: true,
                      },
                      {
                        title: "大小",
                        dataIndex: "size_bytes",
                        width: 100,
                        render: formatBytes,
                      },
                    ]}
                  />
                </div>
              ) : (
                selected.job.state === "succeeded" && (
                  <Empty
                    image={Empty.PRESENTED_IMAGE_SIMPLE}
                    description="未找到对应的解析结果"
                  />
                )
              )}
            </Space>
          )
        )}
      </Drawer>
    </Layout>
  );
}

export default function App() {
  return (
    <AntApp>
      <Dashboard />
    </AntApp>
  );
}
