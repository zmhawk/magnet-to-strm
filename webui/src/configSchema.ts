export type ConfigGroup =
  | "storage"
  | "http"
  | "p115"
  | "ingest"
  | "auth"
  | "logging";
type SettingSection =
  | "database"
  | "http"
  | "p115"
  | "library"
  | "ingest"
  | "logging";
export type CredentialKey =
  | "p115_refresh_token"
  | "aria2_rpc_secret"
  | "qbittorrent_username"
  | "qbittorrent_password";

interface FieldBase {
  group: ConfigGroup;
  label: string;
  required?: boolean;
  help?: string;
}

export type SettingField = FieldBase & {
  source: "settings";
  path: [SettingSection, string];
  defaultValue?: string;
} & (
    | { input: "text" }
    | { input: "number"; precision?: number }
    | { input: "select"; options: { value: string; label: string }[] }
  );

export interface CredentialField extends FieldBase {
  source: "credentials";
  key: CredentialKey;
  input: "password";
}

export type ConfigField = SettingField | CredentialField;

export const configGroups: { id: ConfigGroup; title: string }[] = [
  { id: "storage", title: "本地存储" },
  { id: "http", title: "HTTP 服务" },
  { id: "p115", title: "115 网盘" },
  { id: "ingest", title: "任务" },
  { id: "auth", title: "接口认证" },
  { id: "logging", title: "日志" },
];

// Display groups are independent of the paths persisted in the configuration file.
// Required status, ordering, labels, controls and default hints all come from here.
export const configFields: ConfigField[] = [
  {
    group: "storage",
    source: "settings",
    path: ["database", "path"],
    label: "数据库文件路径",
    required: true,
    input: "text",
    help: "相对路径以程序工作目录为基准",
  },
  {
    group: "storage",
    source: "settings",
    path: ["library", "strm_dir"],
    label: "STRM 输出目录",
    required: true,
    input: "text",
    help: "例如 strms",
  },
  {
    group: "http",
    source: "settings",
    path: ["http", "listen_addr"],
    label: "监听地址",
    required: true,
    input: "text",
    help: "例如 :8081 或 127.0.0.1:8081",
  },
  {
    group: "http",
    source: "settings",
    path: ["http", "public_base_url"],
    label: "对外访问地址",
    required: true,
    input: "text",
    help: "例如 http://127.0.0.1:8081，用于生成 STRM 链接",
  },
  {
    group: "http",
    source: "settings",
    path: ["http", "redirect_base_url"],
    label: "代理目标地址",
    input: "text",
    help: "留空使用对外访问地址",
  },
  {
    group: "http",
    source: "settings",
    path: ["http", "redirect_type"],
    label: "播放重定向方式",
    required: true,
    input: "select",
    options: [
      { value: "direct", label: "直连" },
      { value: "proxy", label: "代理" },
    ],
    help: "直连使用 115 下载链接，代理通过稳定路径播放",
  },
  {
    group: "http",
    source: "settings",
    path: ["http", "materialize_cache_ttl"],
    label: "文件解析缓存时间",
    input: "text",
    defaultValue: "10m",
    help: "例如 30m",
  },
  {
    group: "p115",
    source: "credentials",
    key: "p115_refresh_token",
    label: "115 Refresh TOKEN",
    required: true,
    input: "password",
  },
  {
    group: "p115",
    source: "settings",
    path: ["p115", "work_dir_id"],
    label: "115 工作目录 ID",
    required: true,
    input: "text",
    help: "用于离线下载和缓存的 115 目录 ID，浏览器打开 115 目标目录后，地址栏中的 cid 参数",
  },
  {
    group: "p115",
    source: "settings",
    path: ["p115", "offline_quota_min_remaining"],
    label: "离线额度保护阈值",
    input: "number",
    precision: 10,
    help: "115 离线额度剩余多少时，本服务不再进行离线下载；0 表示关闭保护",
  },
  {
    group: "p115",
    source: "settings",
    path: ["p115", "token_refresh_ahead"],
    label: "TOKEN 提前刷新时间",
    input: "text",
    defaultValue: "10m",
    help: "例如 10m",
  },
  {
    group: "p115",
    source: "settings",
    path: ["p115", "request_rate"],
    label: "每秒请求数",
    input: "number",
    help: "0 表示不限制",
  },
  {
    group: "p115",
    source: "settings",
    path: ["p115", "request_burst"],
    label: "允许的突发请求数",
    input: "number",
    precision: 0,
    help: "非负整数",
  },
  {
    group: "p115",
    source: "settings",
    path: ["p115", "request_concurrency"],
    label: "请求并发数",
    input: "number",
    precision: 0,
    help: "非负整数",
  },
  {
    group: "p115",
    source: "settings",
    path: ["p115", "offline_poll_interval"],
    label: "离线任务轮询间隔",
    input: "text",
    defaultValue: "5s",
    help: "例如 5m",
  },
  {
    group: "p115",
    source: "settings",
    path: ["p115", "offline_poll_min_interval"],
    label: "最短轮询间隔",
    input: "text",
    defaultValue: "2s",
    help: "例如 2s",
  },
  {
    group: "p115",
    source: "settings",
    path: ["p115", "offline_poll_max_interval"],
    label: "最长轮询间隔",
    input: "text",
    defaultValue: "5m",
    help: "例如 5m",
  },
  {
    group: "p115",
    source: "settings",
    path: ["library", "cache_retention"],
    label: "缓存保留时间",
    required: true,
    input: "text",
    help: "已离线到 115 的资源，多久后自动清理。例如 168h",
  },
  {
    group: "p115",
    source: "settings",
    path: ["library", "cache_max_size"],
    label: "缓存空间上限",
    required: true,
    input: "text",
    help: "115 缓存目录的最大占用空间，超出后会按最久未访问的文件清理。例如 4TB；0 表示不限制",
  },
  {
    group: "p115",
    source: "settings",
    path: ["library", "sweep_interval"],
    label: "缓存清理间隔",
    input: "text",
    defaultValue: "1h",
    help: "例如 1h",
  },
  {
    group: "ingest",
    source: "settings",
    path: ["ingest", "job_timeout"],
    label: "单个任务超时时间",
    input: "text",
    defaultValue: "6h",
    help: "例如 6h",
  },
  {
    group: "auth",
    source: "credentials",
    key: "aria2_rpc_secret",
    label: "aria2 RPC 密钥",
    input: "password",
  },
  {
    group: "auth",
    source: "credentials",
    key: "qbittorrent_username",
    label: "qBittorrent 用户名",
    input: "password",
  },
  {
    group: "auth",
    source: "credentials",
    key: "qbittorrent_password",
    label: "qBittorrent 密码",
    input: "password",
  },
  {
    group: "logging",
    source: "settings",
    path: ["logging", "level"],
    label: "日志级别",
    input: "select",
    defaultValue: "info",
    options: ["debug", "info", "warn", "error"].map((value) => ({
      value,
      label: value,
    })),
    help: "日常使用建议选择 info",
  },
];

export const groupedFields = configGroups.map((group) => ({
  ...group,
  fields: configFields
    .filter((field) => field.group === group.id)
    .sort((a, b) => Number(Boolean(b.required)) - Number(Boolean(a.required))),
}));

export const credentialFields = configFields.filter(
  (field): field is CredentialField => field.source === "credentials",
);

export function fieldID(field: ConfigField) {
  return field.source === "settings"
    ? field.path.join(".")
    : `credentials.${field.key}`;
}
