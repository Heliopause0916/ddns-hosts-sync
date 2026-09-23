# ddns-hosts-sync 详细设计规格（DSD v1.0）

> 上游文档：[docs/ARCHITECTURE.md](ARCHITECTURE.md)（《ddns-hosts-sync 架构设计方案》）

> 前级文档《ddns-hosts-sync 架构设计方案》定义了总体拓扑与决策。本文档下沉到 **Go struct 级数据模型、包级函数签名、边界条件矩阵、GUI 组件树、协议细节、可测试验收清单**，供 developer 直接开工。引用前级决策时不再复述理由。

---

## 1. 数据模型（Go struct 级完整定义）

所有 struct 放 `internal/model`。YAML 与 JSON 序列化标签一致（字段名 `snake_case`）。

### 1.1 GlobalConfig

```go
type GlobalConfig struct {
    Version          int        `yaml:"version"`
    Enabled          bool       `yaml:"enabled"`
    IntervalMinutes  int        `yaml:"interval_minutes"`
    DNS              DNSConfig  `yaml:"dns"`
    MaxCNAMEDepth    int        `yaml:"max_cname_depth"`
    FailureKeepOld   bool       `yaml:"failure_keep_old"`
    FlushDNS         bool       `yaml:"flush_dns"`
}

type DNSConfig struct {
    Mode        string   `yaml:"mode"`          // "doh" | "system"
    DOHServers  []string `yaml:"doh_servers"`
    TimeoutSec  int      `yaml:"timeout_sec"`
    IPVersion   string   `yaml:"ip_version"`    // "ipv4" | "ipv6" | "both"
}

func DefaultGlobalConfig() GlobalConfig
func (g *GlobalConfig) Normalize() // 非法值夹取到合法范围（任务端降级用）
func (g *GlobalConfig) Validate() error // 严格校验（GUI 保存用），返回首条错误
```

| 字段 | Go 类型 | 约束 / 默认值 | 说明 |
|---|---|---|---|
| `version` | int | 恒为 1；读入 ≠1 时任务端回退默认、GUI 横幅提示迁移 | 配置 schema 版本 |
| `enabled` | bool | 默认 true | GUI"暂停自动同步"写此字段；任务唤醒读 false 即跳过分发 |
| `interval_minutes` | int | [1, 1440]，默认 5 | 全局统一轮询间隔；**任务注册粒度固定 1 分钟，此值仅做进程内到点判断** |
| `dns.mode` | string | `"doh"` \| `"system"`，默认 doh | doh 时走 DoH 通道 + 自动回退系统 |
| `dns.doh_servers` | []string | 默认 `[cloudflare, dns.google]`；非空校验 | 依次尝试，全部失败回落系统解析 |
| `dns.timeout_sec` | int | [5,120]，默认 15 | 单条目全链路超时（所有查询通道合计） |
| `dns.ip_version` | string | `"ipv4"` \| `"ipv6"` \| `"both"`，默认 ipv4 | both 时 A/AAAA 并行查询 |
| `max_cname_depth` | int | [1,30]，默认 10 | CNAME 链最大深度 |
| `failure_keep_old` | bool | 恒 true（v1 锁定，字段保留） | 解析失败保留 hosts 旧条目 |
| `flush_dns` | bool | 默认 true | 写盘成功后执行系统 DNS 缓存刷新 |

### 1.2 Entry

```go
type Entry struct {
    ID      string `yaml:"id"`
    Source  string `yaml:"source"`
    Target  string `yaml:"target"`
    Enabled bool   `yaml:"enabled"`
    Note    string `yaml:"note"`
}
```

| 字段 | Go 类型 | 约束 / 默认值 | 说明 |
|---|---|---|---|
| `id` | string | 创建时生成 8 字符 UUID 短码；全局唯一、终身不变 | 条目稳定标识，status 关联用；**改名/改域名不换 id** |
| `source` | string | 合法 FQDN（多标签、连字符；不得含 `_` 空格）；保存时 lower | 解析源：跟随 CNAME 链直至 A/AAAA |
| `target` | string | 合法 FQDN 且**不得为 IP 字面量**；保存时 lower | 写入 hosts 的域名；可与 source 相同 |
| `enabled` | bool | 默认 true | 停用条目：不解析、不写、**不删除** hosts 已有行 |
| `note` | string | ≤200 字符 | 仅 GUI 展示 |

### 1.3 Trigger / TriggerAction

```go
type Trigger struct {
    Version     int           `json:"version"`
    RequestID   string        `json:"request_id"`
    RequestedAt time.Time     `json:"requested_at"` // RFC3339 UTC
    Action      TriggerAction `json:"action"`
}

type TriggerAction string

const (
    ActionSyncNow     TriggerAction = "sync_now"      // GUI"立即同步"
    ActionReconfigure TriggerAction = "reconfigure"   // 预留：配置类重注册请求
)
```

| 字段 | 类型 | 约束 | 说明 |
|---|---|---|---|
| `version` | int | 恒 1 | 协议版本 |
| `request_id` | string | UUID 串 | 幂等去重/消费后归档命名 |
| `requested_at` | time.Time | RFC3339 UTC | 任务侧新鲜度判断基准（≤2 分钟视为有效） |
| `action` | TriggerAction | 枚举两值 | 消费分支 |

### 1.4 SyncStatus（顶层）

```go
type SyncStatus struct {
    Version         int              `json:"version"`
    UpdatedAt       time.Time        `json:"updated_at"`
    NextScheduledAt time.Time        `json:"next_scheduled_at"`
    IntervalMinutes int              `json:"interval_minutes"`
    SyncWindow      SyncWindowState  `json:"sync_window"`
    Task            TaskStatus       `json:"task"`
    Entries         []EntryStatus    `json:"entries"`
    HostsBlock      HostsBlockStatus `json:"hosts_block"`
}

type TaskStatus struct {
    LastRunAt time.Time `json:"last_run_at"`
    LastRunOK bool      `json:"last_run_ok"`
    LastError string    `json:"last_error"`
}

type SyncWindowState string

const (
    WindowWaiting SyncWindowState = "waiting" // 本轮醒来未到间隔，未干活
    WindowSynced  SyncWindowState = "synced"  // 本轮执行了完整同步
)
```

### 1.5 EntryStatus + 状态枚举全集

```go
type EntryStatus struct {
    ID                 string     `json:"id"`
    Enabled            bool       `json:"enabled"`
    Status             EntryState `json:"status"`
    ResolvedIPs        []string   `json:"resolved_ips"`
    ResolvedCNAMEChain []string   `json:"resolved_cname_chain"`
    ResolvedAt         time.Time  `json:"resolved_at"`
    UsedFallback       bool       `json:"used_fallback"`
    HostsPresent       bool       `json:"hosts_present"`
    Error              string     `json:"error"`
}

type EntryState string

const (
    EntryOK            EntryState = "ok"
    EntryNXDomain      EntryState = "nxdomain"
    EntryCNAMELoop     EntryState = "cname_loop"
    EntryTooDeep       EntryState = "too_deep"
    EntryTimeout       EntryState = "timeout"
    EntryWriteFailed   EntryState = "write_failed"
    EntryPaused        EntryState = "paused"
    EntryInvalidConfig EntryState = "invalid_config"
)
```

| 枚举值 | 含义 |
|---|---|
| `ok` | 解析成功且需写内容已写入 hosts（或与现有块一致） |
| `nxdomain` | 解析链上出现 NXDOMAIN |
| `cname_loop` | 检测到 CNAME 环（error 含环上节点） |
| `too_deep` | 超过 `max_cname_depth` |
| `timeout` | 所有解析通道失败/超时 |
| `write_failed` | 解析成功但 hosts 写盘失败 |
| `paused` | `enabled=false` 被跳过 |
| `invalid_config` | source/target 非法或 target 冲突（GUI 已拦截，此为任务端手工冲突兜底） |

`UsedFallback`：解析最终成功但走了 system 回退——GUI 用以展示"（回退）"小字提示。注意它是**成功路径的注解**，不改变绿标判定。

### 1.6 HostsBlockStatus

```go
type HostsBlockStatus struct {
    Present     bool      `json:"present"`
    ContentMD5  string    `json:"content_md5"`  // 当前 hosts 文件中块的实际 md5
    ExpectedMD5 string    `json:"expected_md5"` // 最近一次期望块文本 md5
    LastWriteAt time.Time `json:"last_write_at"`
    LastWriteOK bool      `json:"last_write_ok"`
}
```

| 字段 | 含义 |
|---|---|
| `present` | 块是否存在于 hosts（markers 是否完整成对） |
| `content_md5` | 本轮读取时的实际块 hex md5 |
| `expected_md5` | 最近一次期望块（未变化时二者相等） |
| `last_write_at` / `last_write_ok` | 最后一次写盘时间与结果 |

### 1.7 配置校验规则表

| # | 规则目标 | 规则 | GUI 端（保存时） | 任务端（运行时降级） |
|---|---|---|---|---|
| 1 | interval | 1 ≤ x ≤ 1440 | 输入框数值过滤 + 保存校验；非法阻止保存并提示区间 | `Normalize()` 夹取到 [1,1440]，写 warning 日志 |
| 2 | source | 合法 FQDN 正则 | 编辑框失焦即时校验（红框 + tooltip），阻止保存 | 该条目标 `invalid_config`，跳过解析 |
| 3 | target | 合法 FQDN 且非 IP 字面量 | 同上 | 上述 |
| 4 | target 重复 | 所有 enabled 条目 target 全局唯一 | 保存时全局校验，提示冲突条目对 | 冲突中仅保留序首个，其余标 `invalid_config`（error 注明天块目标域名） |
| 5 | YAML 语法 | 可被 yaml.v3 解析 | 打开窗口显示红色横幅 + "导出坏文件"按钮 | 回退 `DefaultGlobalConfig()`（entries 空），`task.last_error` 记录；不写回 |
| 6 | max_cname_depth | 1 ≤ x ≤ 30 | 输入框校验 | `Normalize()` 夹取 |
| 7 | timeout_sec | 5 ≤ x ≤ 120 | 输入框校验 | `Normalize()` 夹取 |
| 8 | ip_version | ∈ {ipv4, ipv6, both} | 下拉框（结构上无非法值） | 非法值回落 `ipv4` |
| 9 | doh_servers | 非空且均为 https URL | 非空校验 + URL 格式校验 | 空/全非法回落默认列表 |
| 10 | note | ≤200 字符 | 软限制：截断并提示 | 任务端不关心 |

GUI 与任务共用 `validateGlobalConfig` / `validateEntry`，保证判定逻辑单源。

---

## 2. 核心包接口与函数签名

### 2.1 internal/resolver（纯逻辑，无平台差异）

```go
package resolver

// ResolveResult 一次完整 CNAME 链解析的结果
type ResolveResult struct {
    IPs           []string // 已排序（见 5.4），A 在前 AAAA 在后
    Chain         []string // 含 source 的完整链：["entry.ddns.net","relay.example.net"]
    FinalName     string   // 最终 A 记录的 owner
    UsedFallback  bool     // 最终经 system 回退命中
    ResolvedAt    time.Time
}

// Resolve：单条目全链路入口。内部：DoH 列表依次查询 → 全败回退系统解析（mode=doh 时）
// 失败返回非 nil error，且必须可通过 errors.Is 匹配下方 sentinel。
func Resolve(source string, cfg model.DNSConfig) (*ResolveResult, error)

// 内部（不导出）：
// queryDOH(server, name, qtype string, timeout time.Duration) (*dohAnswer, error) // application/dns-json，纯标准库
// querySystem(name, qtype string, timeout time.Duration) ([]string, error)       // net.Resolver + net.Resolver.PreferGo=false

// 错误类型（Sentinel errors，调试/UI 分类依据）：
var (
    ErrCNAMELoop   = errors.New("resolver: cname loop detected")
    ErrTooDeep     = errors.New("resolver: cname chain exceeds max depth")
    ErrNXDomain    = errors.New("resolver: nxdomain")
    ErrTimeout     = errors.New("resolver: all resolution channels timed out")
    ErrInvalidName = errors.New("resolver: invalid hostname")
)
```

Sentinel 错误约定：`errors.Is` 为唯一判定手段；包装时用 `fmt.Errorf("resolve %q: %w", source, ErrTimeout)` 保留上下文链。

### 2.2 internal/hostsfile（字节级操作，不解释块外内容）

```go
package hostsfile

const (
    BeginMarker = "# BEGIN ddns-hosts-sync"
    EndMarker   = "# END ddns-hosts-sync"
)

type Line struct { // 一条待写记录（已校验）
    IP     string
    Target string
}

type WriteReport struct {
    Changed    bool      // 块内容是否变化（changed=false 不写盘）
    ContentMD5 string    // 写后块 md5
    EOL        string    // 检出的换行符（"\r\n"|"\n"），写盘沿用
}

func Read(path string) (content []byte, err error) // 不存在返回 os.ErrNotExist

// Pulse：marker 所在行索引（begin,end），返回 found=false 表示缺块
func LocateBlock(content []byte) (begin, end int, found bool)

// MD5Block：对给定字节区间计算 md5（hex）
func MD5Block(content []byte) (string, error)

// BuildBlock：Lines → 规范化块文本（不含 markers，见 5.3 规范规则）
func BuildBlock(lines []Line, order model.IPOrder) ([]byte, error)

// ComposeFull：content + 期望块 → 新全文（缺块则追加文末；有块则替换区间）
// 返回 changed：full 文本是否与原文不同（逐字节）
func ComposeFull(content []byte, block []byte) (full []byte, changed bool, err error)

func WriteAtomic(path string, full []byte, eol string) error // temp+fsync+rename，同目录
func Backup(path string) (backupPath string, err error)      // 复制为 hosts.bak-ddns-hosts-sync
var ErrWriteFailed = errors.New("hostsfile: write failed")   // 包装底层错误
```

**关键设计**：`Read`/`LocateBlock` 全部以 `[]byte` 操作，marker 用字节锚定，**块外内容原样字节复制**——规避 Windows hosts 非 UTF-8 编码问题（见 7.2）。

### 2.3 internal/sync（同步编排，无 GUI 依赖）

```go
package sync

type Options struct {
    ConfigPath  string
    StateDir    string // status.json / trigger.json 所在目录
    LogPath     string
    HostsPath   string
    Force       bool   // 忽略间隔门，立即完整同步（trigger sync_now 置 true）
    IntervalTick int   // 任务注册粒度（固定 60s），供等待窗口判断
}

func Run(opts Options) (final *model.SyncStatus, err error) // 一次完整循环，sync 子命令入口
```

**Run 状态机（运行流程序列）**：

1. 初始化：解析入参 → 定位 `state/status.json`、`state/trigger.json`、`logs/sync.log`；
2. 日志：打开 append 句柄，检查滚动（>1MB 重建，见 5.5）；
3. 读 config：失败 → `DefaultGlobalConfig()` 兜底 + `task.last_error` 记录；成功 → `Normalize()`；
4. 消费 trigger：`state.ConsumeTrigger` → 新鲜（≤2min）且 `action=sync_now` 则 `Force=true`；`reconfigure` 仅记日志（v1 无消费方）；
5. 间隔门：`!Force && now.Before(lastRunAt+interval)` → 写最小 status（`sync_window=waiting`、刷新 `updated_at`），**exit 0**；
6. 完整同步：
   - a. 遍历 entries（顺序执行，保证 status 顺序与 config 一致）：
     - `enabled=false` → `EntryPaused`，跳过解析；
     - `enabled=true` → `resolver.Resolve` → 成功则 `EntryOK`；失败则 `EntryNXDomain/CNAMELoop/TooDeep/Timeout`（按 errors.Is 匹配），`resolved_ips=[]`；
   - b. 装配期望块（`hostsfile.BuildBlock`，仅取 OK 条目，IP 排序见 5.4）；
   - c. 读 hosts → `LocateBlock` → 计算 `content_md5` → `expected_md5`；
   - d. **变化检测**：期望块 md5 与当前块 md5 相等且块存在 → **不写盘**，仅更新条目 `HostsPresent=true`、刷新时间戳；
   - e. 有变化 → `Backup`（失败仅 warn）→ `ComposeFull` → `WriteAtomic`（失败重试 3 次，500ms backoff）→ `flush_dns`（失败仅 warn）；
   - f. 写失败时：`task.LastRunOK=false`、`hosts_block.LastWriteOK=false`、对应条目标 `EntryWriteFailed`，**旧 hosts 内容保留原样**（rename 未发生）；
7. 写 status.json（temp+rename）；写日志摘要行 `<total> entries, <changed>/<failed>`;
8. **统一 exit 0**：DNS/写盘失败一律编码进 status 而非退出码（调度器不因退出码重试；非零仅用于程序自身致命错误，如 state 目录不可写）。

### 2.4 internal/state（原子读写 + 消费语义）

```go
package state

// 通用原子写：`<name>.<pid>.<nanotime>.tmp` 写入同目录 → fsync → rename 覆盖
func AtomicWriteJSON(path string, v any) error
func AtomicWriteFile(path string, data []byte) error

func ReadJSON[T any](path string) (T, bool, error) // bool=false 表示文件不存在

// 状态：唯一写者=任务；GUI 直接轮询读，容忍 rename 窗口内瞬时失败（抽干一次 50ms 重试）
func WriteStatus(path string, s *model.SyncStatus) error
func ReadStatus(path string) (*model.SyncStatus, bool, error)

// 触发：唯一写者=GUI；唯一消费者=任务；消费=读到后 rename 至 trigger.consumed.<requestID>.json 再删除
func WriteTrigger(path string, t *model.Trigger) error
func ConsumeTrigger(path string) (*model.Trigger, bool, error) // bool=是否消费到有效文件

// 新鲜度：requested_at 距今 ≤ maxAge（调用方传 2*time.Minute）
func IsFresh(t *model.Trigger, now time.Time, maxAge time.Duration) bool
```

### 2.5 internal/tasks（计划任务三平台接口）

```go
package tasks

type TaskSpec struct {
    Name        string   // 注册名：ddns-hosts-sync
    ExecPath    string   // 程序绝对路径
    Args        []string // ["sync"]
}

type Manager interface {
    Install(spec TaskSpec) error                                    // schtasks /create /xml /f；launchd plist；systemd timer
    Uninstall(name string) error
    Trigger(name string) error    // schtasks /run（尽力，失败静默）；launchd kickstart；systemctl start
    Exists(name string) (bool, error)
    IsSupported() bool            // 不支持的 OS 返回 false（install 子命令转而为提示）
}

func New() Manager // 依据 runtime.GOOS 编译期选择
```

### 2.6 internal/platform（平台抽象，被 cmd 与 sync/tray 消费）

```go
package platform

type Platform interface {
    // ── 路径 ──
    HostsPath() string
    DataDir() string              // %ProgramData%\ddns-hosts-sync
    ConfigPath() string           // DataDir/config/config.yaml
    StateDir() string
    LogPath() string
    // ── 系统动作 ──
    FlushDNSCache() error
    // ── 任务 ──
    InstallTask(spec tasks.TaskSpec) error
    UninstallTask(name string)
    TriggerTask(name string)
    TaskExists(name string) (bool, error)
    // ── 自启/GUI ──
    InstallTrayAutostart() error   // HKCU Run / launchd LaunchAgent
    RemoveTrayAutostart() error
    // ── 安装期 ──
    SetupAllDirs() error           // 创建目录树 + 设置 ACL（先建后授权，见 7.4）
    ExecPath() string              // os.Executable()
    IsAdmin() bool
}
```

**导入方向硬约束**：`cmd/ddns-hosts-sync` 是唯一分发点（argv 分派 `tray|sync|install|uninstall|version`）；`sync` 包不 import `tray/gui`；`tray/gui` 不 import `hostsfile/resolver`。`tasks`/`platform` 的 OS 分文件用 build tag：`platform_windows.go` / `platform_darwin.go` / `platform_linux.go`。

---

## 3. 边界条件与错误处理表

### 3.1 DNS 类

| 场景 | 期望行为 | status.json 落盘字段 | GUI 呈现（图标/徽标/文案） |
|---|---|---|---|
| 全超时（DoH+system 均失败） | 保留 hosts 旧值（若存在），不写盘 | `entry.status=timeout`, `resolved_ips=[]`, `hosts_present`（有旧值=true）, `error="解析超时（15s），保留上一次条目"` | 托盘黄；条目徽标"超时"；tooltip 显示错误摘要 |
| 部分 DoH 失败→system 成功 | 正常按结果写盘 | `entry.status=ok`, `UsedFallback=true`, `error="DoH 不可用已回退系统解析"` | 托盘绿；条目徽标"OK(回退)"小字；tooltip 见 error |
| NXDOMAIN | 保留旧值；不写新条目 | `status=nxdomain`, `hosts_present` 保持, `error="解析链上不存在域名 xxx"` | 托盘黄；徽标"NXDOMAIN" |
| CNAME 环 | 保留旧值 | `status=cname_loop`, `error="检测到 CNAME 环: a→b→a"` | 托盘黄；徽标"CNAME环"；tooltip 显示链 |
| 超深度 | 按 max 层截断后失败 | `status=too_deep`, `error="CNAME 链超过 10 层"` | 托盘黄 |
| 解析结果变化 | 写盘 + flushdns | `hosts_block.expected_md5` 更新、`last_write_at` 刷新、`entry.resolved_ips` 新值、`task.last_run_ok=true` | 托盘绿；日志行 `changed=true`；状态面板显示"最近写入时间" |
| 解析结果不变 | **不写盘**，仅刷新时间戳 | `content_md5==expected_md5`、`hosts_block` 不变、`updated_at` 刷新、`entry.status=ok` | 托盘绿；不产生任何写盘事件 |

### 3.2 hosts 类

| 场景 | 期望行为 | status 落盘 | GUI 呈现 |
|---|---|---|---|
| 块被用户删/改 | 块缺失或 md5 不符 → 按期望块重建（托管语义：下轮自愈）；记录"已修复"的 last_error 级 warn | 若本轮修复成功：`present=true`、`content_md5` 恢复、`expected_md5` 一致、`last_write_at` 刷新；`task.last_error="检测到块缺失/篡改，已自动重写"` | 托盘黄（本轮有 warn）→ 下轮变绿；状态面板"块已自动修复"提示 |
| hosts 文件不存在 | 视为空内容处理 → 创建 | `hosts_block.present=false` → 写后 `true`；写失败走下行 | 绿（写入成功）或红（写失败） |
| 写失败（权限/磁盘满/AV 拦截） | 保留旧 hosts（rename 未发生），重试 3 次 | `task.last_run_ok=false`、`task.last_error=...`、`hosts_block.last_write_ok=false`、条目标 `EntryWriteFailed` | **托盘红**；状态面板红色告警条 |
| 备份失败 | 不阻断写盘，仅记 warn | `task.last_error` 前缀 `warn: backup 失败...`（run_ok 仍 true） | 托盘黄（含 warn） |
| flushdns 失败 | 写盘成功即算完成，缓存下轮再 flush | `task.last_error="warn: flushdns 失败..."` | 托盘黄；tooltip 提示 |

### 3.3 配置类

| 场景 | 期望行为 | status 落盘 | GUI 呈现 |
|---|---|---|---|
| YAML 非法 | GUI：横幅 + 导出副本；任务：回退默认 | `task.last_error="config 解析失败: ..."`, `entries=[]` | GUI 红横幅；托盘黄 |
| source/target 字段非法 | GUI 保存拦截；任务端若遇手工错误 config 跳过该条目 | `status=invalid_config`, `error` 描述 | 黄；条目行内红框提示 |
| target 重复 | GUI 保存拦截（主防线） | 任务端若遇到：首个保留，其余 `status=invalid_config`, error 指明冲突目标 | 黄（GUI 已拦截则不会出现） |
| interval 越界 | GUI 输入框拦截 | 任务端 `Normalize()` 夹取，interval_minutes 落被夹取值 | 黄提示"已夹取到范围内" |
| entry.enabled=false | 不解析、不写、**不删除** hosts 已有行 | `status=paused`, `hosts_present` 保持 | 条目灰显"已暂停"；托盘不因其变黄 |

### 3.4 运行类

| 场景 | 期望行为 | status 落盘 | GUI 呈现 |
|---|---|---|---|
| 托盘多实例 | 单实例锁（Windows Mutex，其他 OS lockfile O_EXCL），后到者弹提示退出 | 无 | 弹窗"程序已在运行" |
| sync 多实例并发（计划任务串行，理论无；留保险） | state 目录内 `sync.lock`（O_EXCL），拿不到等 5s 后 exit 0 | 无 | 不呈现 |
| trigger 过期 | 消费时 IsFresh=false → 忽略并删除，记 INFO 日志 | 无 | 无 |
| 计划任务未注册但托盘运行 | 启动时 `TaskExists=false` → 灰色状态 + 配置窗口顶部引导条 | task 字段不写（GUI 自检） | 托盘灰；tooltip"自动同步未安装，请联系管理员运行 install.exe" |
| 时间回拨 | 全部时间戳 UTC；间隔门用 `now.Sub(lastRunAt)` 负值视同未到点，不做负间隔计算 | `next_scheduled_at` 由 `now+interval` 重算 | 无 |
| GUI 编辑 config 期间任务在跑 | 任务一次性读入 config 快照于内存，写盘窗口不冲突 | 本次任务使用旧快照，下轮生效 | 无竞态可见 |

---

## 4. GUI 详细规格

### 4.1 fyne 配置窗口组件树

```
Window "ddns-hosts-sync 配置"                     widget.NewWindow
└── container.NewBorder
    ├── top    : Toolbar
    │    新增｜编辑｜删除｜上移｜下移｜启用/停用　||　立即同步
    ├── bottom : StatusBar：Label 最近同步=xx ｜ 下次=xx ｜ 横幅(warn/err，Appearance 变色)
    └── center : container.NewAppTabs
         ├─ Tab "条目"   : widget.NewTable(6 列)
         │     列1 启用(Check)｜列2 源域名｜列3 目标域名｜列4 状态(Icon+Label 复合)｜列5 最近IP(join"\n")｜列6 备注
         ├─ Tab "全局设置" : Form
         │     轮询间隔(Entry)→ValidatableEntry| DNS模式(Select)｜DoH服务器(Entry 多行, 每行一个)
         │     IP版本(Select)｜DNS刷新(Check)｜暂停自动同步(Check)｜ [保存] [恢复默认]
         └─ Tab "状态"    : VBox 只读 Labels（见 4.4 映射） + [查看日志目录] [查看最近200行]
```

技术要点：
- 表格数据：`[]model.Entry`（config 顺序）+ `map[string]model.EntryStatus`（由 status.json 每 10s 轮询，mtime 变化才重解析并 `table.Refresh()`，避免全量重建）；
- 复选框变更即写 config（自动保存语义：条目操作实时持久，无需"保存"按钮）；全局设置区明确放置 [保存]（批量事务）；
- 立即同步按钮在执行后置 disabled 10s 防连点。

### 4.2 条目列表交互

| 操作 | 行为 |
|---|---|
| 排序 | 默认 config 顺序；列头点击排序不支持（v1），仅提供工具栏上移/下移（交换后整体写回 config） |
| 新增 | 对话框 Form：源/目标/备注/启用；点确定时整体校验（含全局 target 去重），失败弹 ValidateError 列表，**不关闭对话框** |
| 编辑 | 同新增校验；id 不变 |
| 删除 | 二次确认对话框；删除后**立即写 config**，hosts 中该行由下次同步清理（若已启用且曾是仅剩行则下次写盘移除该行） |
| 启用/停用 | 切换即写 config（自动持久化） |
| 校验时机 | 失焦即时校验（红框+tooltip）做体验校验；**提交时执行权威校验**（ValidateError 汇总） |

### 4.3 托盘四色状态机（优先级裁定）

```
若 status.json 缺失 或 超过 max(2×interval, 6h) 未更新（含 GUI 启动首查）：
    → 灰（"后台同步未运行，请检查计划任务"）
否则：
    1. 任一红条件：task.last_run_ok=false 且 last_error 含写盘错误
                  │ hosts_block.last_write_ok=false
                  │ hosts_block.present=false 且 len(entries)>0
        → 红（最高优先级，覆盖一切）
    2. 任一黄条件：∃ entry.status ∈ {nxdomain, cname_loop, too_deep, timeout, invalid_config}
                │ last_error 以 "warn:" 开头
        → 黄
    3. 其余 → 绿
```

裁定顺序自上而下，命中即取该色（红>黄>绿，灰独立通道）。tooltip 恒显示：最近同步时间 + 首条告警摘要。

### 4.4 状态面板字段 → status.json 映射

| 展示项 | 来源字段 |
|---|---|
| 最近同步 | `updated_at` |
| 下次同步 | `next_scheduled_at` |
| 本轮窗口 | `sync_window`（"等待中/已同步"） |
| 任务健康 | `task.last_run_ok` / `task.last_error` |
| hosts 块状态 | `hosts_block.present` / `content_md5 vs expected_md5` / `last_write_ok` / `last_write_at` |
| 条目状态徽标 | `entries[].status`（含 error 全文 tooltip） |
| CNAME 链 | `entries[].resolved_cname_chain` join `" -> "` |
| 回退标记 | `entries[].UsedFallback` → "（回退解析）"后缀 |
| 最近 IP | `entries[].resolved_ips` join `", "` |

### 4.5 立即同步完整时序

```
1. GUI 生成 request_id=UUID；构造 Trigger{ActionSyncNow, now}
2. state.WriteTrigger(state/trigger.json)  ← temp+rename
3. platform.TriggerTask("ddns-hosts-sync") ← schtasks /run；失败静默（普通用户常被拒）
4. 按钮提示："已请求立即同步，最迟 1 分钟完成"；按钮 disabled 10s
5. 任务下一分钟醒来：ConsumeTrigger → fresh & sync_now → Force=true → 完整同步 → 写 status
6. GUI 每 10s 轮询 status.json：updated_at 前进
   │  → 刷新面板/托盘颜色，横幅"同步完成"
   ├── 若 3 分钟无前进：横幅红"后台任务未响应"
   └──（兜底：若 /run 被拒，step 5 仍保证 ≤1 分钟完成）
```

---

## 5. 协议与文件细节

### 5.1 版本演进与文件约定

- **status/trigger version=1，读端宽容**：向后兼容策略——读端忽略未知字段；仅当 `version > 已知最大` 时降级（GUI 显示"版本过新，建议升级"；任务端仍解释已知字段）。新字段一律 optional；breaking 变更一律升 version 并写迁移函数。config 的 `version != 1` 见 1.1。
- 临时文件命名：`<basename>.<pid>.<nanotime>.tmp`，写完 `fsync` 再 `rename` 覆盖目标。启动时清理 state 目录中 mtime>1h 的 `*.tmp`（崩溃残留自愈）。
- 消费协议：trigger 由任务 `rename` 为 `trigger.consumed.<requestID>.json` 后删除，恰好消费一次；GUI 永不读 trigger。
- **ACL 目标**（Windows，install 时 icacls 设置，顺序见 7.4）：
  - 根 `%ProgramData%\ddns-hosts-sync\`：SYSTEM F / Administrators F / **Users R+列目录**；
  - `config\`：追加 `Users:(OI)(CI)M`（GUI 免提权写）；
  - `state\`、`logs\`：SYSTEM F / Administrators F / Users R（**GUI 只读**，防误写状态）。

### 5.2 计划任务 XML schema 要点（Windows）

```xml
<Task version="1.2">
  <RegistrationInfo><Description>ddns-hosts-sync background sync</Description></RegistrationInfo>
  <Triggers>
    <BootTrigger><Enabled>true</Enabled></BootTrigger>               <!-- 触发器1：开机 -->
    <TimeTrigger>                                                  <!-- 触发器2：每分钟重复 -->
      <StartBoundary>2026-01-01T00:00:00</StartBoundary>
      <Repetition><Interval>PT1M</Interval><Duration>PT0S</Duration>
      <StopAtDurationEnd>false</StopAtDurationEnd></Repetition>
      <Enabled>true</Enabled>
    </TimeTrigger>
  </Triggers>
  <Principals><Principal id="Author">
    <UserId>S-1-5-18</UserId>   <!-- SYSTEM -->
    <RunLevel>HighestAvailable</RunLevel>
  </Principal></Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <ExecutionTimeLimit>PT10M</ExecutionTimeLimit>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
  </Settings>
  <Actions><Exec>
    <Command>"C:\Program Files\ddns-hosts-sync\ddns-hosts-sync.exe"</Command>
    <Arguments>sync</Arguments>
  </Exec></Actions>
</Task>
```

- 注册：`schtasks /Create /TN "ddns-hosts-sync" /XML <file> /F`；注销 `/Delete /F`；触发 `/Run`。
- **XML 用 `encoding/xml` 生成，禁止手工字符串拼接**；路径与 Arguments 独立字段、引号交由 Task Scheduler 处理。SYSTEM 账户无 UserProfile——所有路径绝对化、程序不依赖 CWD。

### 5.3 hosts 块规范化规则

- 行格式 `IP<TAB>Target`；解析用正则 `^(\S+)\s+(\S+)$`，写出统一 `\t`；
- IP 排序：IPv4 字典序（`net.ParseIP` + `To4().String()`）→ IPv6 字典序（`To16()`）；both 模式下 A 行恒在 AAAA 行前；
- Target 小写，一行一条；多条 IP 时逐行；
- marker 独占整行，**无后缀注释**（保证字节锚点唯一）；空块（无 OK 条目）保留两条 marker（present=true 可检测）；
- 换行符沿用文件现有（detect 首个换行为 `\r\n` 或 `\n`，无则默认 `\r\n`——Windows 优先）；**块外字节不做任何解码/重写**。

### 5.4 日志格式与轮转

```
2026-09-24T10:00:05+08:00 INFO  [e-01] resolved 203.0.113.42 chain="a->b->c" used_fallback=false
2026-09-24T10:00:05+08:00 WARN  [] backup failed: <err>
2026-09-24T10:00:06+08:00 ERR   [] hosts write failed after 3 retries: <err>
2026-09-24T10:00:06+08:00 INFO  [] summary entries=2 changed=1 failed=0
```

- 单行文本 `时间 UTC±8 LEVEL [entry_id] msg`，LEVEL ∈ {INFO, WARN, ERR}；标准库 `log` 扩展，无第三方依赖（`err` 输出到 `os.Stderr` 并行）；
- 轮转：任务每次启动检查文件大小 >1MB → 当前文件 rename 为 `sync.log.1`（覆盖旧）→ 重建新文件。短生命周期进程无句柄滞留问题。

---

## 6. 验收标准（可测试清单）

### M0 — 骨架与协议冻结
- [ ] `git init` 已有（保留 .git）；`go mod init github.com/heliopause/ddns-hosts-sync`
- [ ] `go build ./...` && `go vet ./...` 零错误
- [ ] 迁移 `chat-export` 至 `docs/`，README 说明来源
- [ ] `go test ./internal/model ./internal/state`：校验规则全例（10 条）通过；`AtomicWriteJSON` 写→读一致性断言；tmp 残留清理逻辑单测
- [ ] `ddns-hosts-sync version` 输出 `v0.1.0+<sha>`（ldflags 注入）

### M1 — 命令行全链路（无 GUI）
- [ ] 单测：`resolver` 用 `httptest` mock DoH——3 跳 CNAME 链正确、环检测命中 `ErrCNAMELoop`、mock 超时命中 `ErrTimeout`、伪造 NXDOMAIN 命中 `ErrNXDomain`
- [ ] 单测：`hostsfile.ComposeFull`——缺块追加文末 / 有块替换 / 块外内容逐字节不变（含非 UTF-8 字节段）断言
- [ ] 集成（管理员 cmderr）：手工 `sync -config=t -hosts=t/hosts` → hosts 出现标记块且 `content_md5==expected_md5`；**立即重跑 mtime 不变**（变化检测生效）
- [ ] 集成：人为删块内容重跑 → 自愈写回；`hosts.bak-ddns-hosts-sync` 存在且为上一版
- [ ] 集成：断网/停 DoH 重跑 → status 各条目 `timeout`，hosts 旧值保留

### M2 — 计划任务 + 托盘 GUI
- [ ] 管理员安装 → `schtasks /Query /TN ddns-hosts-sync` 输出 Scheduled；`/rl highest` 确认
- [ ] 注销登录态下运行 5 个周期仍更新 status.json 与 hosts（LocalSystem 免登录证据）
- [ ] GUI：新增条目 ≤1 分钟生效写 hosts；target 重复被保存拦截；停用条目 hosts 行保留不删
- [ ] 立即同步：点按钮 → ≤1 分钟完成；正常情况下（管理员测试机）`/run` 即时生效
- [ ] 托盘颜色：断网→黄；人为改坏块→本轮黄"已自动修复"；停用计划任务→灰（陈旧）；恢复→绿
- [ ] 多实例：开两个 tray → 第二个弹提示退出
- [ ] 卸载：任务消失、`hosts` 还原（块删除）、ProgramData 清理、`logs` 副本留在安装目录

### M3 — 平台适配与发布
- [ ] `.goreleaser.yml`：windows amd64/arm64 zip + macOS tar.gz + linux tar.gz 三产物构建通过；Windows 二进制带图标 resource
- [ ] macOS 真机：launchd 任务每 60s 触发、`/etc/hosts` 更新、`dscacheutil -flushcache` 生效
- [ ] 文档走查：非技术同事按 `user-guide.md`（解压→右键管理员运行 install→完成）成功率 100%

---

## 7. 实现约束与风险（developer 必读）

1. **fyne 托盘 API 现状坑**：`fyne.io/systray`（或 fyne drawer）在 Windows 与 `fyne` 窗口同进程存在焦点/退出的历史问题。**推荐结构：systray 库独立管理图标与菜单循环，fyne 仅渲染配置窗口**（`app.NewWithID`，不调用 fyne 自带 dock/tray）。图标右键菜单必须**先于**窗口创建，且 tray callbacks 全部入主 goroutine。
2. **Windows hosts 编码**（最高风险）：hosts 普遍为 ANSI/UTF-8 无 BOM，可能含本地编码字节。**全程 `[]byte` 操作，块外内容绝不 decode/re-encode**；对比、替换、md5 全在字节域完成。Go 的标准库文件 API 返回原始字节，天然满足——严禁在 hostsfile 路径上引入任何 "转 string" 语义假设。
3. **schtasks XML**：用 `encoding/xml` 生成而非字符串拼接；`PT0S` 表示无限重复；`/RU SYSTEM` 写死 SID `S-1-5-18`；执行行带引号路径。**中文/空格路径**由 Command 字段承接，Arguments 不含任何相对路径；任务恢复验证做"无 CWD 依赖"断言（在 `%WINDIR%` 启动程序仍正常）。
4. **ProgramData ACL**：`icacls` 必须先 `mkdir /A` 建目录再授权（对已存在目录用 `/T` 递归）；顺序：先 SYSTEM/Administrators 完全控制，**后追加** Users 项（继承策略逐层生效）；`config` 的 Users M 继承到 state/logs 时须在子目录单独收口（子目录重新设置只读，覆盖父继承）。
5. **DoH `application/dns-json`**：cloudflare/dns.google 均需该 Accept；企业代理可能改写响应；**回退链必须按"HTTP 错误 → 下一个服务器 → 全部失败 → 系统解析"**逐步，不能一个 404 即判超时。
6. **Windows rename 与 AV/EDR**：hosts 是安全软件重点监控对象，`os.Rename` 可能瞬时 EACCES/EPERM → **重试 3×500ms**；若仍失败落 `write_failed` 状态，绝不提示用户干预。
7. **`ipconfig /flushdns` 输出编码**：中文系统为 GBK，**只判 err，不解析 stdout**（避免乱码）；`exec.Command` 加 `cmd /C` 包装防 shell 注入（无变量传入则省略）。
8. **表性能**：fyne Table 条目数 >100 时用 `table.UpdateCell` 长效缓存（`widget/cell` 接口），避免每 10s 全表 `Refresh` 重建 widget。
9. **单实例锁**：Windows 用命名 Mutex（`golang.org/x/sys/windows` 或 syscall），其他平台 fallback `open(O_CREATE|O_EXCL)` lockfile；锁释放用 defer，防托盘异常退出残留。
10. **陈旧判定封顶**：托盘灰度阈值 `max(2×interval, 6h)`——防止用户配置 1440 分钟间隔后误报灰。
11. **构建隔离**：`tray`/`gui` 包带 `//go:build !windows` 之外还需区分——**任务二进制不得链接 fyne**：用 `internal/gui` 仅被 `cmd` 的 tray 分支 import，sync 分支包路径本身不含 fyne，靠依赖图天然隔离，勿加 build tag 造成双编译复杂度。

---

## 附：与已确认决策的一致性核对

| 已确认决策 | 本规格落点 |
|---|---|
| 托盘常驻 + 配置窗口 | 4.1~4.4，tray 常驻纯用户态 |
| 源/目标分离 | 1.2（source/target）、2.1 解析仅 source |
| 双进程（UI 用户态 / 提权写 hosts） | 1.1 拓扑 + sync 组织在 LocalSystem 计划任务 |
| 状态文件/触发文件通信、禁命名管道 | 2.4 state 包，单向双文件，无管道 |
| 全局统一间隔 | 1.1 interval_minutes，任务注册粒度固定 1min |
| 仅管标记块 | 2.2 / 5.3 字节域 marker 锚定 |
| DoH 优先 / 标准库为主 | 2.1 纯标准库 `application/dns-json`；唯一第三方为 `yaml.v3` |
| 唯一一次 UAC | install/uninstall 一次性；运行期提权全走系统任务 |
| 普通同事零交互 | 1.1e/f 失败不弹窗、保留旧值；5.1 ACL 免提权写配置；6 验收含非技术走查 |

以上即为 DSD v1.0 全部内容，可直接作为 developer 开工依据（工作量估计按 M0→M1→M2→M3 顺序推进，其中 M2 为产品主体）。
