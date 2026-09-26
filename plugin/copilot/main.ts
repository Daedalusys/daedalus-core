/**
 * Daedalus OS Copilot CLI 编排器：交互式 REPL 与单次查询编排。
 * 轮次循环：转换 -> 解析验证 -> y/n 确认/编辑/拒绝反馈循环 -> 执行。
 * 建议先行：所有命令先展示 + 风险标注；白名单内只读诊断确认后沙箱运行，
 * 白名单外只展示（非 TTY 仅白名单只读诊断运行）；事务通道与 shell 沙箱
 * 通道互斥（见 ★ 事务通道 ★ 段）。每个生命周期事件严格落哈希链审计。
 */

import {
  parseProposal,
  classifyProposal,
  classifyTxProposal,
  L0_WHITELIST,
  validateProposal,
  buildSystemPrompt,
  type CommandProposal,
  type RiskAssessment,
} from "./policy.ts";
import { recordAudit, type AuditOutcome } from "./audit.ts";
import { readConfig, translate, revise, type Config } from "./llm.ts";
import {
  execAllowlisted,
  execTxPropose,
  execTxPreview,
  execTxApply,
  type ExecResult,
  type TxProposeOutcome,
  type TxPreviewOutcome,
  type TxApplyOutcome,
} from "./exec.ts";
import { initI18n, t, currentLocale } from "./i18n.ts";

export const VERSION = "1.0.0";

export interface ParsedArgs {
  yes: boolean;
  verbose: boolean;
  interactive: boolean;
  dryRun: boolean;
  provider?: string;
  model?: string;
  baseUrl?: string;
  help: boolean;
  version: boolean;
  query: string;
}

export interface CopilotIO {
  isTerminal?: boolean;
  writeStdout: (text: string) => Promise<void> | void;
  writeStderr: (text: string) => Promise<void> | void;
  readLine: (promptText?: string) => Promise<string | null>;
  readStdinAll?: () => Promise<string>;
}

export interface CopilotOptions {
  args?: string[];
  query?: string;
  yes?: boolean;
  verbose?: boolean;
  interactive?: boolean;
  dryRun?: boolean;
  provider?: string;
  model?: string;
  baseUrl?: string;
  isTerminal?: boolean;
  stdinReader?: (prompt?: string) => Promise<string | null>;
  stdout?: { write: (str: string) => void | Promise<void> };
  stderr?: { write: (str: string) => void | Promise<void> };
  // 用于单元测试的可注入依赖项
  translateFn?: typeof translate;
  reviseFn?: typeof revise;
  execFn?: typeof execAllowlisted;
  recordAuditFn?: typeof recordAudit;
  readConfigFn?: typeof readConfig;
  // 事务通道注入点:begin 本文件实现,propose/preview/apply 直承 exec.ts execTx*,测试可 mock
  txBeginFn?: typeof defaultTxBegin;
  txProposeFn?: typeof execTxPropose;
  txPreviewFn?: typeof execTxPreview;
  txApplyFn?: typeof execTxApply;
  // state 记忆注入点:镜像 translateFn 可 stub 形态,默认 readStateSummary(直读 state.jsonl,永不 throw)
  readStateFn?: typeof readStateSummary;
}

// 事务通道
//
// ★ 编码约定 ★ LLM 以 CommandProposal 形态提议事务:command 取保留动词
// "tx.propose"/"tx.apply"/"tx.rollback",args = [目标, 期望态?](rollback 仅
// tx-id,期望态缺省空串)。detectTxIntent 命中即改走 classifyTxProposal 定级,
// shell 分类器绝不参与事务意图——两条执行通道互不竞争(竞态条款)。
//
// ★ begin 归属 ★ exec.ts 只交付 execTxPropose/Preview/Apply,均不自动 begin;
// daedalus-tx CLI propose 走 mustLoad(空日志即 not found),故事务通道必须显式
// begin 先行。本文件直接 spawn `daedalus-tx begin`(argv 直传;二进制解析链与
// exec.ts resolveTxBinary 逐项镜像)。

/** 事务提议三元组:classifyTxProposal 的入参形态（intent 为下划线 token）。 */
interface TxIntent {
  intent: "tx_propose" | "tx_apply" | "tx_rollback";
  target: string;
  desiredState: string;
}

/** v1 事务适配器之一;与 daedalus-tx 侧 service.set 词汇逐字一致。 */
const TX_ADAPTER_SERVICE_SET = "service.set";

/** v1 事务适配器之二;与 daedalus-tx 侧 package.set 适配器名逐字一致。 */
const TX_ADAPTER_PACKAGE_SET = "package.set";

/**
 * 判定事务目标是否属于 package 域:与 classifyTxProposal 同款正则,命中剥
 * "package " 前缀返回裸包名(后续 propose 的 args JSON `{name, desired_state}`
 * 只收裸名)。导出仅供 tests 钉分类器/路由双侧域判定一致性。
 */
export function packageTargetName(target: string): string | null {
  const m = /^package\s+(\S+)$/.exec(target.trim());
  return m ? m[1] : null;
}

/** begin 看门狗毫秒数:与 execAllowlisted / execTx* 缺省 40s 同源常量。 */
const TX_BEGIN_TIMEOUT_MS = 40000;

/** 保留动词 → 事务 intent 映射（静态表,勿运行时改写）。 */
const TX_COMMAND_INTENTS: Record<string, TxIntent["intent"]> = {
  "tx.propose": "tx_propose",
  "tx.apply": "tx_apply",
  "tx.rollback": "tx_rollback",
};

/**
 * 判定提议是否为事务意图。命中保留动词 → {intent, target, desiredState};
 * 否则 null(走 shell 路径)。target/desiredState 原样透传给 classifyTxProposal
 * ——空 target 由其抛 "classifyTxProposal: target is empty"(fail-closed),
 * 调用方不得先行吞错。
 */
export function detectTxIntent(proposal: CommandProposal): TxIntent | null {
  const intent = TX_COMMAND_INTENTS[proposal.command];
  if (!intent) {
    return null;
  }
  return {
    intent,
    target: proposal.args[0] ?? "",
    desiredState: proposal.args[1] ?? "",
  };
}

/** begin 结果:成功仅携带 16-hex tx_id;失败仅携带错误文本（永不 throw）。 */
export type TxBeginOutcome =
  | { ok: true; txId: string }
  | { ok: false; error: string };

// tx_id 形状门（crypto/rand 8 字节 hex）;形态不符即解析失败。
const TX_ID_RE = /^[a-f0-9]{16}$/;

/**
 * 解析 begin 用的 daedalus-tx 二进制路径:与 exec.ts resolveTxBinary 镜像同链
 * (DAEDALUS_TX_BIN → 生产 /usr/local/bin/daedalus-tx → 仓库 dev 产物)。
 * exec.ts 解析器未导出,此处为刻意最小镜像;若后续补 execTxBegin 则收敛回单一实现。
 */
function resolveTxBeginBinary(): string {
  const envPath = typeof (globalThis as any).Deno?.env?.get === "function"
    ? (globalThis as any).Deno.env.get("DAEDALUS_TX_BIN")
    : (globalThis as any).process?.env?.DAEDALUS_TX_BIN;
  if (envPath) {
    return envPath;
  }

  const exists = (target: string): boolean => {
    try {
      (globalThis as any).Deno.statSync(target);
      return true;
    } catch {
      return false;
    }
  };

  const productionPath = "/usr/local/bin/daedalus-tx";
  if (exists(productionPath)) {
    return productionPath;
  }
  for (const rel of [
    "daedalus-core/bin/daedalus-tx",
    "../daedalus-core/bin/daedalus-tx",
  ]) {
    if (exists(rel)) {
      return rel;
    }
  }
  return productionPath;
}

/**
 * `daedalus-tx begin` —— 开事务日志并取回 tx_id(stdout 契约:恰一份 JSON
 * 文档 {"tx_id":"<16hex>"});失败归一化为 {ok:false,error},永不 throw,由
 * runTxTurn 经 t("tx.error.run") 渲染。导出用于单元测试注入假二进制验证。
 */
export async function defaultTxBegin(): Promise<TxBeginOutcome> {
  const CommandConstructor = (globalThis as any).Deno?.Command;
  if (!CommandConstructor) {
    return {
      ok: false,
      error: "Deno.Command is not available in the current runtime environment",
    };
  }

  const binary = resolveTxBeginBinary();
  let output: any;
  try {
    // argv 直传 begin 子命令,无 /bin/sh 包装;超时走 AbortSignal（一次性 CLI,
    // 无 stdio 桥,不需要 execAllowlisted 的 activeProcesses 回收链）。
    output = await new CommandConstructor(binary, {
      args: ["begin"],
      stdin: "null",
      stdout: "piped",
      stderr: "piped",
      signal: AbortSignal.timeout(TX_BEGIN_TIMEOUT_MS),
    }).output();
  } catch (err: unknown) {
    return {
      ok: false,
      error: `Failed to spawn daedalus-tx begin: ${err instanceof Error ? err.message : String(err)}`,
    };
  }

  const decoder = new TextDecoder();
  const stdout = decoder.decode(output.stdout ?? new Uint8Array());
  const stderr = decoder.decode(output.stderr ?? new Uint8Array());

  if (typeof output.code === "number" && output.code !== 0) {
    // CLI 失败契约:出错也吐 {"error":...};优先取文档 error 字段
    const errText = stdout.trim() || stderr.trim() ||
      `daedalus-tx begin exited with code ${output.code}`;
    return { ok: false, error: errText };
  }

  // stdout 契约:单行紧凑 JSON,单层转义,JSON.parse 一次即得终值
  const line = stdout.split("\n").map((l) => l.trim()).filter((l) => l.length > 0)[0];
  if (!line) {
    return { ok: false, error: "daedalus-tx begin stdout 为空(契约要求恰一份 JSON 文档)" };
  }
  try {
    const doc = JSON.parse(line) as { tx_id?: unknown };
    if (typeof doc?.tx_id !== "string" || !TX_ID_RE.test(doc.tx_id)) {
      return { ok: false, error: `daedalus-tx begin 输出缺少合法 tx_id: ${line.slice(0, 200)}` };
    }
    return { ok: true, txId: doc.tx_id };
  } catch {
    return { ok: false, error: `daedalus-tx begin stdout 解析失败: ${line.slice(0, 200)}` };
  }
}

// state 记忆读取:copilot 是 internal/state 追加式观测缓存 state.jsonl 的
// 用户态读取方,翻译前把每资源最新观测注入 system prompt 追加段(KNOWN STATE)。
//
// ★ 为什么不走 daedalus-service MCP ★ v1 state 按上下文隔离(KNOWN
// LIMITATION):daemon 态 service 跑在 DynamicUser 命名空间,其写入的
// /var/lib/daedalus 实际落宿主 /var/lib/private/daedalus,用户态读不到;
// service.query 是实时 systemctl 查询,manifest --allow-run 亦未放行。
// 消费面与 internal/dirs.File 链逐字镜像:DAEDALUS_STATE_PATH(env 独占)→
// /var/lib/daedalus/state.jsonl → $HOME/.local/share/daedalus/state.jsonl。
//
// ★ 错误分类 ★ NotFound 静默(新镜像/demo 正常态);PermissionDenied 及其它
// 真实 I/O 错记入 errors[] 由 runQueryTurn 打 stderr 后继续(配置期 bug 必须
// 可见)。两者不写审计:state 是派生缓存不是证据。readStateSummary 永不 throw。

/** 摘要中的一条 service 观测(internal/state StateEntry + objectmodel.ServiceState 的消费侧形状)。 */
export interface StateSummaryEntry {
  /** 单元名(含 .service 后缀,systemctl 原文)。 */
  name: string;
  /** 观测时刻(StateEntry.observed_at 原文,UTC RFC3339Nano)。 */
  observedAt: string;
  /** 声明期望态(ServiceState.desired_state,查询路径恒空)。 */
  desiredState: string;
  /** systemctl 属性载荷(ServiceState.properties,键为属性名原文)。 */
  properties: Record<string, string>;
}

/** readStateSummary 的结果。text 为空 entries 时是兜底文案(经调用方 entries.length 过滤,不进 prompt)。 */
export interface StateSummary {
  /** 每个资源名最新一条 service 观测,按 name 升序(镜像 LatestByKind 的 newest-wins + 确定性排序)。 */
  entries: StateSummaryEntry[];
  /** system prompt 追加块(KNOWN STATE);entries 为空时为 state.summary.empty 兜底文案。 */
  text: string;
  /** PermissionDenied / 其它真实 I/O 错的路径清单(调用方负责 stderr 呈现,随后继续)。 */
  errors: string[];
}

/** dirs 链的系统级缺省落位(与 internal/dirs.StateSystemFile 逐字一致)。 */
const STATE_SYSTEM_FILE = "/var/lib/daedalus/state.jsonl";

/** dirs 链的 $HOME 相对落位(与 internal/dirs.StateHomeRel 逐字一致)。 */
const STATE_HOME_REL = ".local/share/daedalus/state.jsonl";

/** 摘要行呈现的属性子集(curated 只读白名单里对提议最相关的三项,顺序固定)。 */
const STATE_SUMMARY_PROPERTIES = ["ActiveState", "SubState", "UnitFileState"];

/** Deno / Node 双运行时环境变量读取(audit.ts 同款守卫形态)。 */
function getEnvValue(name: string): string | undefined {
  const deno = (globalThis as any).Deno;
  if (typeof deno?.env?.get === "function") {
    return deno.env.get(name) ?? undefined;
  }
  return (globalThis as any).process?.env?.[name];
}

/**
 * 解析 state.jsonl 候选路径链。env 存在即独占(Go 侧 dirs.File 的 env 段
 * 命中后不再下探;相对路径 env 值在生产由 Go 写入方硬拒,TS 读取方按
 * 普通候选处理——读不到即 NotFound 静默,属良性)。env 未设时依次尝试
 * 系统路径 → $HOME 路径($HOME 缺失兜底 /root,与 audit.ts 同款)。
 */
function resolveStatePathCandidates(): string[] {
  const envPath = getEnvValue("DAEDALUS_STATE_PATH");
  if (envPath) {
    return [envPath];
  }
  const home = getEnvValue("HOME") || "/root";
  return [STATE_SYSTEM_FILE, `${home}/${STATE_HOME_REL}`];
}

/**
 * 单条观测的摘要行。属性 token(ActiveState=active 等)与单元名是喂给
 * LLM 的机器可读数据,保留英文原文(与 JSON 协议字段同理,非 UI 文案);
 * 行模板本身经 i18n state.summary.line 走。
 */
function summarizeEntryLine(entry: StateSummaryEntry): string {
  const shown = STATE_SUMMARY_PROPERTIES
    .filter((k) => typeof entry.properties?.[k] === "string" && entry.properties[k] !== "")
    .map((k) => `${k}=${entry.properties[k]}`)
    .join(", ");
  const detail = shown || (entry.desiredState ? `desired_state=${entry.desiredState}` : "-");
  return t("state.summary.line", entry.name, detail, entry.observedAt || "-");
}

/**
 * 渲染 system prompt 追加块。entries 为空返回 state.summary.empty 兜底
 * 文案("no prior state memory")——是否注入由调用方按 entries.length
 * 决定,空态绝不污染 prompt。
 */
export function formatStateSummary(entries: StateSummaryEntry[]): string {
  if (entries.length === 0) {
    return t("state.summary.empty");
  }
  return [t("state.summary.header"), ...entries.map(summarizeEntryLine)].join("\n");
}

/**
 * 解析 state.jsonl 全文:kind=service 的条目按名 newest-wins(append-only
 * 文件"后写即新",与 Go LatestByKind 同判据),输出按 name 升序。
 * 容错姿态与 Go Read 一致(缓存≠证据):非法 JSON / 截断行 / 非对象行 /
 * 字段类型不符一律静默跳过,绝不因单行损坏报错。
 */
function parseStateEntries(text: string): StateSummaryEntry[] {
  const latest = new Map<string, StateSummaryEntry>();
  for (const rawLine of text.split("\n")) {
    const line = rawLine.trim();
    if (!line) {
      continue;
    }
    let obj: unknown;
    try {
      obj = JSON.parse(line);
    } catch {
      continue; // 坏行静默跳过
    }
    const e = obj as Record<string, unknown> | null;
    if (!e || typeof e !== "object" || e.kind !== "service" || typeof e.name !== "string") {
      continue; // 非 service 条目 / 结构不符
    }
    const payload = (e.payload && typeof e.payload === "object"
      ? e.payload
      : {}) as Record<string, unknown>;
    const properties: Record<string, string> = {};
    if (payload.properties && typeof payload.properties === "object") {
      for (const [k, v] of Object.entries(payload.properties as Record<string, unknown>)) {
        if (typeof v === "string") {
          properties[k] = v;
        }
      }
    }
    latest.set(e.name, {
      name: e.name,
      observedAt: typeof e.observed_at === "string" ? e.observed_at : "",
      desiredState: typeof payload.desired_state === "string" ? payload.desired_state : "",
      properties,
    });
  }
  return [...latest.values()].sort((a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0));
}

/**
 * 读取 state 记忆缓存(best-effort,永不 throw)。NotFound 静默;
 * PermissionDenied / 其它真实 I/O 错记入 errors 后继续下探下一候选。
 * 导出用于 runQueryTurn 的默认注入实现与单元测试直调。
 */
export async function readStateSummary(): Promise<StateSummary> {
  const deno = (globalThis as any).Deno;
  if (typeof deno?.readTextFile !== "function") {
    // 非 Deno 运行时(Node 测试宿主等):无 FS 即无状态,静默降级
    return { entries: [], text: t("state.summary.empty"), errors: [] };
  }
  const errors: string[] = [];
  let content: string | null = null;
  for (const candidate of resolveStatePathCandidates()) {
    try {
      content = await deno.readTextFile(candidate);
      break;
    } catch (err: unknown) {
      const name = (err as { name?: string })?.name ?? "";
      if (name === "NotFound") {
        continue; // 缺失 = 正常态(v1 新镜像 / demo 无写入方)
      }
      // PermissionDenied 与其它 I/O 错:开发者可见,记录后继续
      errors.push(candidate);
    }
  }
  const entries = content === null ? [] : parseStateEntries(content);
  return { entries, text: formatStateSummary(entries), errors };
}

export function parseArgs(argv: string[]): ParsedArgs {
  const result: ParsedArgs = {
    yes: false,
    // 默认开启 verbose:先打印翻译结果(→ cmd + explanation)再执行,
    // 翻译后到运行前用户能 Ctrl-C 取消。-v 显式关闭预览(白名单只读诊断仍按既定路径处理)。
    verbose: true,
    interactive: false,
    dryRun: false,
    help: false,
    version: false,
    query: "",
  };

  const positional: string[] = [];
  let i = 0;
  while (i < argv.length) {
    const arg = argv[i];
    if (arg === "-y" || arg === "--yes") {
      result.yes = true;
      i++;
    } else if (arg === "-v" || arg === "--verbose") {
      // 显式 -v 切换:默认开(显示),-v 关闭(静默),保留用户对历史 -v 用法的兼容。
      result.verbose = !result.verbose;
      i++;
    } else if (arg === "-i" || arg === "--interactive") {
      result.interactive = true;
      i++;
    } else if (arg === "--dry-run") {
      result.dryRun = true;
      i++;
    } else if (arg === "-h" || arg === "--help") {
      result.help = true;
      i++;
    } else if (arg === "-V" || arg === "--version") {
      result.version = true;
      i++;
    } else if (arg === "-p" || arg === "--provider") {
      if (i + 1 < argv.length) {
        result.provider = argv[i + 1];
        i += 2;
      } else {
        i++;
      }
    } else if (arg.startsWith("--provider=")) {
      result.provider = arg.slice("--provider=".length);
      i++;
    } else if (arg === "-m" || arg === "--model") {
      if (i + 1 < argv.length) {
        result.model = argv[i + 1];
        i += 2;
      } else {
        i++;
      }
    } else if (arg.startsWith("--model=")) {
      result.model = arg.slice("--model=".length);
      i++;
    } else if (arg === "--base-url" || arg === "--baseUrl") {
      if (i + 1 < argv.length) {
        result.baseUrl = argv[i + 1];
        i += 2;
      } else {
        i++;
      }
    } else if (arg.startsWith("--base-url=")) {
      result.baseUrl = arg.slice("--base-url=".length);
      i++;
    } else if (arg.startsWith("--baseUrl=")) {
      result.baseUrl = arg.slice("--baseUrl=".length);
      i++;
    } else if (arg === "--") {
      positional.push(...argv.slice(i + 1));
      break;
    } else {
      positional.push(arg);
      i++;
    }
  }

  result.query = positional.join(" ").trim();
  return result;
}

function checkIsTerminal(): boolean {
  if (typeof (globalThis as any).Deno?.stdin?.isTerminal === "function") {
    try {
      return (globalThis as any).Deno.stdin.isTerminal();
    } catch {
      return false;
    }
  }
  return Boolean((process.stdin as any)?.isTTY);
}

let globalStdinBuffer = "";
const globalStdinDecoder = new TextDecoder();

async function defaultReadLineFromStdin(): Promise<string | null> {
  while (true) {
    const idx = globalStdinBuffer.indexOf("\n");
    if (idx !== -1) {
      const line = globalStdinBuffer.slice(0, idx).replace(/\r$/, "");
      globalStdinBuffer = globalStdinBuffer.slice(idx + 1);
      return line;
    }

    if (typeof (globalThis as any).Deno?.stdin?.read === "function") {
      const buf = new Uint8Array(1024);
      const n = await (globalThis as any).Deno.stdin.read(buf);
      if (n === null || n === 0) {
        if (globalStdinBuffer.length > 0) {
          const remaining = globalStdinBuffer.replace(/\r$/, "");
          globalStdinBuffer = "";
          return remaining;
        }
        return null;
      }
      globalStdinBuffer += globalStdinDecoder.decode(buf.subarray(0, n));
    } else {
      try {
        const fs = require("fs");
        const buf = Buffer.alloc(1024);
        const n = fs.readSync(0, buf, 0, 1024, null);
        if (n === 0) {
          if (globalStdinBuffer.length > 0) {
            const remaining = globalStdinBuffer.replace(/\r$/, "");
            globalStdinBuffer = "";
            return remaining;
          }
          return null;
        }
        globalStdinBuffer += buf.toString("utf-8", 0, n);
      } catch {
        return null;
      }
    }
  }
}

/**
 * 管道输入时从标准输入读取全部内容的回退方法。Deno 2 已移除
 * `Deno.readAll`,必须用 `Deno.stdin.readable` 异步迭代逐块累积;
 * 读取异常返回空串由调用方处理空查询。导出仅用于单元测试注入。
 */
export async function defaultReadStdinAll(): Promise<string> {
  const deno = (globalThis as any).Deno;
  if (deno?.stdin?.readable) {
    try {
      const decoder = new TextDecoder();
      let text = "";
      for await (const chunk of deno.stdin.readable as ReadableStream<Uint8Array>) {
        text += decoder.decode(chunk, { stream: true });
      }
      // 结束时无参 decode 冲刷解码器残留字节
      text += decoder.decode();
      return text.trim();
    } catch {
      return "";
    }
  }
  try {
    const fs = require("fs");
    return fs.readFileSync(0, "utf-8").trim();
  } catch {
    return "";
  }
}

function createIO(options?: CopilotOptions): CopilotIO {
  const isTerminal = options?.isTerminal ?? checkIsTerminal();
  const encoder = new TextEncoder();

  const writeStdout = async (text: string) => {
    if (options?.stdout) {
      await options.stdout.write(text);
      return;
    }
    if (typeof (globalThis as any).Deno?.stdout?.write === "function") {
      await (globalThis as any).Deno.stdout.write(encoder.encode(text));
    } else if (typeof process?.stdout?.write === "function") {
      process.stdout.write(text);
    }
  };

  const writeStderr = async (text: string) => {
    if (options?.stderr) {
      await options.stderr.write(text);
      return;
    }
    if (typeof (globalThis as any).Deno?.stderr?.write === "function") {
      await (globalThis as any).Deno.stderr.write(encoder.encode(text));
    } else if (typeof process?.stderr?.write === "function") {
      process.stderr.write(text);
    }
  };

  const readLine = async (promptText?: string): Promise<string | null> => {
    if (options?.stdinReader) {
      return await options.stdinReader(promptText);
    }
    if (typeof (globalThis as any).prompt === "function" && isTerminal) {
      // Deno 的 prompt() 接受提示文本作为第一个参数；
      // 若不传，会显示裸的默认提示符 "Prompt "，吞掉确认提示语。
      // 兜底提示文案同样走 i18n（调用方未传 promptText 时）。
      const line = (globalThis as any).prompt(promptText ?? t("confirm.prompt"));
      return line;
    }
    if (promptText) {
      await writeStdout(promptText);
    }
    return await defaultReadLineFromStdin();
  };

  return {
    isTerminal,
    writeStdout,
    writeStderr,
    readLine,
    readStdinAll: defaultReadStdinAll,
  };
}

/** CLI 使用说明;全部文案经 t() 走 i18n,拼接保证 locale 切换后连贯。 */
export function getHelpMessage(): string {
  return [
    t("help.banner"),
    "",
    t("help.usage"),
    t("help.usage_pattern"),
    "",
    t("help.default_behavior"),
    "",
    t("help.options"),
    t("help.flag.verbose"),
    t("help.flag.interactive"),
    t("help.flag.yes"),
    t("help.flag.dry_run"),
    t("help.flag.provider"),
    t("help.flag.model"),
    t("help.flag.base_url"),
    t("help.flag.version"),
    t("help.flag.help"),
    "",
    t("help.examples"),
    t("help.example.run"),
    t("help.example.dry_run"),
    t("help.example.interactive"),
    t("help.example.repl"),
  ].join("\n") + "\n";
}

interface TurnContext {
  query: string;
  yes: boolean;
  verbose: boolean;
  interactive: boolean;
  dryRun: boolean;
  configOverrides: Partial<Config>;
  isTerminal: boolean;
  io: CopilotIO;
  translateFn: typeof translate;
  reviseFn: typeof revise;
  execFn: typeof execAllowlisted;
  recordAuditFn: typeof recordAudit;
  readConfigFn: typeof readConfig;
  // 事务通道依赖(见 CopilotOptions 同名注入点)
  txBeginFn: typeof defaultTxBegin;
  txProposeFn: typeof execTxPropose;
  txPreviewFn: typeof execTxPreview;
  txApplyFn: typeof execTxApply;
  // state 记忆读取依赖(见 CopilotOptions 同名注入点)
  readStateFn: typeof readStateSummary;
}

/** renderLLMError 返回的渲染结果:stderr 文案 + 分类 + 透传字段(供审计条件展开)。 */
interface RenderedLLMError {
  msg: string;
  kind: string;
  fields: {
    endpoint?: string;
    timeoutMs?: number;
    status?: number;
    body?: string;
    err?: string;
  };
}

/** 无 kind 属性错误(意外异常)的兜底分类常量,与测试对齐;非 llm.ts ErrorKind 枚举成员。 */
const ERROR_KIND_UNKNOWN = "unknown";

/**
 * 消费 llm.ts 的结构化 LLM 错误,渲染为本地化可操作文案。上游形状为
 * Error + 附加属性(kind ∈ timeout|http|network|config,fields 携带
 * endpoint/timeoutMs/status/body/err);本函数用类型守卫读取,无 kind
 * (意外异常)走兜底:t("error.translate" 原样透传 + error_kind="unknown")。
 * timeout 文案占位 {1} 为秒:Math.round(timeoutMs/1000)(1000ms → "1")。
 */
function renderLLMError(err: unknown): RenderedLLMError {
  const originalMsg = err instanceof Error ? err.message : String(err);
  const e = err as { kind?: unknown; fields?: unknown } | null;
  const kind = typeof e?.kind === "string" ? e.kind : "";
  const fields = (e?.fields && typeof e.fields === "object"
    ? e.fields
    : {}) as RenderedLLMError["fields"];

  switch (kind) {
    case "timeout": {
      const timeoutSec = Math.round((fields.timeoutMs ?? 0) / 1000);
      return {
        msg: t("error.llm.timeout", fields.endpoint ?? "", timeoutSec),
        kind,
        fields,
      };
    }
    case "http":
      return {
        msg: t("error.llm.http", fields.endpoint ?? "", fields.status ?? "", fields.body ?? ""),
        kind,
        fields,
      };
    case "network":
      return {
        msg: t("error.llm.network", fields.endpoint ?? "", fields.err ?? originalMsg),
        kind,
        fields,
      };
    case "config":
      // 固定文案(提示设 key),无占位符
      return { msg: t("error.llm.config"), kind, fields };
    default:
      return {
        msg: t("error.translate", originalMsg),
        kind: ERROR_KIND_UNKNOWN,
        fields,
      };
  }
}

/**
 * 构造 copilot_error 审计 args:核心三键恒定,fields 明细条件展开(无值即
 * 无键)——保持 args 为可 JSON 序列化普通对象,不留 null/undefined 噪音。
 */
function buildErrorAuditArgs(
  query: string,
  rendered: RenderedLLMError,
): Record<string, unknown> {
  const { kind, fields } = rendered;
  return {
    query,
    error: rendered.msg,
    error_kind: kind,
    ...(fields.endpoint ? { endpoint: fields.endpoint } : {}),
    // timeout_ms 仅在 timeout/http 场景上报(llm.ts 两类都带该字段)
    ...(((kind === "timeout" || kind === "http") && typeof fields.timeoutMs === "number")
      ? { timeout_ms: fields.timeoutMs }
      : {}),
    ...(kind === "http" && typeof fields.status === "number" ? { status: fields.status } : {}),
  };
}

/**
 * 事务通道轮次:begin → propose(快照无副作用)→ preview(计划前→计划后 diff)
 * → y/n → apply(真实副作用)。仅 safe 分级(L0)事务意图可达本函数;
 * caution/danger 事务在 runQueryTurn 展示分流处提前拦截(★ L1/L2 事务绝不
 * apply ★)。begin/propose/apply 消费 exec.ts execTx* 结构化返回(永不
 * throw),失败经 t("tx.error.run") 渲染并落 copilot_error 审计,绝不吐栈。
 * "n" 放弃:日志留 proposed 态(无副作用无需回滚),审计 copilot_tx_reject 携带
 * reason "user denied tx"(audit token 字面量)。v1 事务不进 [e]dit/[n] 修订
 * 循环——修订通道属 shell 提议,事务反馈路径是重新发起一次查询。
 */
async function runTxTurn(
  ctx: TurnContext,
  proposal: CommandProposal,
  txIntent: TxIntent,
): Promise<number> {
  const {
    query,
    yes,
    isTerminal,
    io,
    recordAuditFn,
    txBeginFn,
    txProposeFn,
    txPreviewFn,
    txApplyFn,
  } = ctx;
  // 与 shell 通道同门:-y / 非 TTY 视为已授权(auto),TTY 默认 y/n
  const requireConfirmation = isTerminal && !yes;

  const begun = await txBeginFn();
  if (!begun.ok) {
    await io.writeStderr(t("tx.error.run", "begin", begun.error) + "\n");
    await recordAuditFn(
      "copilot_error",
      { query, stage: "begin", error: begun.error },
      "error",
    );
    return 1;
  }
  const txId = begun.txId;

  // propose 按域路由:target 形如 "package <name>" 走 package.set(args 只收
  // 裸包名),其余 service.set;两类均仅快照 Before/AfterState,无副作用。
  const pkgName = packageTargetName(txIntent.target);
  const txAdapter = pkgName !== null ? TX_ADAPTER_PACKAGE_SET : TX_ADAPTER_SERVICE_SET;
  const txTargetName = pkgName ?? txIntent.target;
  const proposed: TxProposeOutcome = await txProposeFn(txId, txAdapter, {
    name: txTargetName,
    desired_state: txIntent.desiredState,
  });
  if (!proposed.ok) {
    await io.writeStderr(t("tx.error.run", "propose", proposed.error) + "\n");
    await recordAuditFn(
      "copilot_error",
      { query, tx_id: txId, stage: "propose", error: proposed.error },
      "error",
    );
    return proposed.returncode || 1;
  }
  await recordAuditFn(
    "copilot_tx_propose",
    {
      query,
      tx_id: txId,
      adapter: txAdapter,
      target: txIntent.target,
      desired_state: txIntent.desiredState,
    },
    "success",
  );

  // 步骤 3:preview —— 计划前→计划后 diff 展示。preview 失败不阻断确认:
  // 步骤已在日志中(上一步 propose 成功),diff 只是展示增益。
  const preview: TxPreviewOutcome = await txPreviewFn(txId);
  if (preview.ok) {
    await io.writeStdout(t("tx.preview.header", txId) + "\n");
    for (const entry of preview.diff) {
      await io.writeStdout(
        t(
          "tx.preview.diff",
          entry.index,
          entry.adapter,
          JSON.stringify(entry.args ?? null),
          JSON.stringify(entry.beforeState ?? null),
          JSON.stringify(entry.afterState ?? null),
        ) + "\n",
      );
    }
  } else {
    await io.writeStderr(t("tx.error.run", "preview", preview.error) + "\n");
  }

  // 步骤 4:y/n 确认(仅 L0;apply 的真实副作用必须由这次 y 授权)
  if (requireConfirmation) {
    while (true) {
      const choiceRaw = await io.readLine(t("tx.prompt.confirm"));
      if (choiceRaw === null) {
        // EOF (Ctrl-D) 视作放弃:日志留 proposed 态,无任何副作用
        await recordAuditFn("copilot_cancel", { query, proposal, tx_id: txId }, "denied");
        return 0;
      }

      const choice = choiceRaw.trim().toLowerCase();
      if (choice === "y" || choice === "yes") {
        break;
      }
      if (choice === "n" || choice === "no") {
        await recordAuditFn(
          "copilot_tx_reject",
          { query, tx_id: txId, reason: "user denied tx" },
          "denied",
        );
        await io.writeStdout(t("tx.reject.dropped") + "\n");
        return 0;
      }
      if (choice === "q" || choice === "quit") {
        await recordAuditFn("copilot_cancel", { query, proposal, tx_id: txId }, "denied");
        return 0;
      }
      await io.writeStdout(t("tx.prompt.invalid") + "\n");
    }
  }

  const applied: TxApplyOutcome = await txApplyFn(txId);
  if (!applied.ok) {
    await io.writeStderr(t("tx.error.run", "apply", applied.error) + "\n");
    await recordAuditFn(
      "copilot_error",
      { query, tx_id: txId, stage: "apply", error: applied.error },
      "error",
    );
    return applied.returncode || 1;
  }
  await io.writeStdout(t("tx.result.applied", txId) + "\n");
  await recordAuditFn(
    "copilot_tx_apply",
    {
      query,
      tx_id: txId,
      target: txIntent.target,
      desired_state: txIntent.desiredState,
      // auto:true = -y / 非 TTY 自动授权;false = y/n 循环显式 "y"
      auto: !requireConfirmation,
    },
    "success",
  );
  return 0;
}

/** 单次查询轮次:转换 → 解析验证 → verbose/dryRun → interactive 确认 → 执行。 */
async function runQueryTurn(ctx: TurnContext): Promise<number> {
  const {
    query,
    yes,
    verbose,
    interactive,
    dryRun,
    configOverrides,
    isTerminal,
    io,
    translateFn,
    reviseFn,
    execFn,
    recordAuditFn,
    readConfigFn,
    readStateFn,
  } = ctx;

  const stateSummary = await readStateFn();
  for (const deniedPath of stateSummary.errors) {
    await io.writeStderr(t("state.summary.error", deniedPath) + "\n");
  }
  const stateBlock = stateSummary.entries.length > 0 ? stateSummary.text : "";

  let config: Config;
  try {
    config = readConfigFn(configOverrides);
  } catch (err: unknown) {
    const msg = err instanceof Error ? err.message : String(err);
    await io.writeStderr(t("error.config", msg) + "\n");
    await recordAuditFn("copilot_error", { query, error: msg }, "error");
    return 1;
  }

  // 步骤 1:翻译。stateBlock 作 system prompt 追加段(KNOWN STATE),空块传
  // undefined,提示词与注入前逐字节一致。
  let rawLlmOutput: string;
  try {
    rawLlmOutput = await translateFn(query, configOverrides, stateBlock || undefined);
  } catch (err: unknown) {
    // W1 结构化错误 → W2 i18n 可操作文案(哪个端点、等了多久、怎么办);
    // 审计带 error_kind 及条件性 endpoint/timeout_ms/status 供链上检索
    const rendered = renderLLMError(err);
    await io.writeStderr(rendered.msg + "\n");
    await recordAuditFn(
      "copilot_error",
      buildErrorAuditArgs(query, rendered),
      "error",
    );
    return 1;
  }

  let proposal: CommandProposal;
  try {
    proposal = parseProposal(rawLlmOutput);
  } catch (err: unknown) {
    const msg = err instanceof Error ? err.message : String(err);
    await io.writeStderr(t("error.policy_reject", msg) + "\n");
    await recordAuditFn(
      "copilot_reject",
      { query, rawOutput: rawLlmOutput, reason: msg },
      "denied",
    );
    return 1;
  }

  // 步骤 2.3:事务意图探测,命中保留动词改走 classifyTxProposal(shell 分类器不参与,竞态条款)
  const txIntent = detectTxIntent(proposal);

  // 步骤 2.4:本地静态风险分级(LLM 不参与风险自标注)。shell:L0 = safe 且
  // 白名单内 → 唯一可能沙箱执行的分级,其余一律展示;tx:静态表定级,
  // tx_propose/tx_apply(started|stopped)= safe → 事务通道,其余绝不 apply。
  let risk: RiskAssessment;
  try {
    risk = txIntent
      ? classifyTxProposal(txIntent.intent, txIntent.target, txIntent.desiredState)
      : classifyProposal(proposal);
  } catch (err: unknown) {
    // 入参防御:空 target 与未知 intent 在此 fail-closed 拒绝——事务连
    // begin 都不发起,零 CLI spawn、零副作用。
    const msg = err instanceof Error ? err.message : String(err);
    await io.writeStderr(t("error.policy_reject", msg) + "\n");
    await recordAuditFn(
      "copilot_reject",
      { query, proposal, reason: msg },
      "denied",
    );
    return 126;
  }

  // 归一契约:shell 路径不携带 tx_kind,缺省即 "shell"(勿写字面比较)。
  const txKind = risk.tx_kind ?? "shell";
  // 事务执行通道准入:必须显式 tx_kind 且定级 safe;caution/danger 事务走展示。
  const isTxChannel = txKind !== "shell" && risk.level === "safe";

  const proposedStr = proposal.args.length > 0
    ? `${proposal.command} ${proposal.args.join(" ")}`
    : proposal.command;

  // 展示路径:caution / danger / safe(白名单外)/ caution-danger 事务。架构上
  // 不可执行——daedalus-shell 拒白名单外命令,这里也不给 y 选项(模式分离的
  // 物理保障)。safe 事务绕开本块进事务通道,其余事务仅展示(★ L1/L2 绝不
  // apply ★)。(danger 的 reasonKey 必非空;safe-outside / caution 亦带 i18n key)
  if (!isTxChannel && (risk.level !== "safe" || !L0_WHITELIST.has(proposal.command))) {
    if (risk.level === "safe") {
      // 白名单外 safe:✓ 标签 + 手动执行提示(如 git --version)
      await io.writeStdout(t("risk.banner.safe") + "\n");
    } else if (risk.level === "caution") {
      await io.writeStdout(t("risk.banner.caution") + "\n");
    } else {
      // danger:🚨 标签 + 原因行仅在 pattern key(reasonKey 非空且非
      // outside_sandbox/caution_command)时渲染,防御性保留。
      await io.writeStdout(t("risk.banner.danger") + "\n");
      if (
        risk.reasonKey &&
        risk.reasonKey !== "risk.reason.outside_sandbox" &&
        risk.reasonKey !== "risk.reason.caution_command"
      ) {
        await io.writeStdout(`${t("risk.reason_label")}${t(risk.reasonKey)}\n`);
      }
    }
    await io.writeStdout(`→ ${proposedStr}\n`);
    if (proposal.explanation) {
      await io.writeStdout(`  ${proposal.explanation}\n`);
    }
    await io.writeStdout(t("risk.manual_hint") + "\n");
    // 展示路径落 copilot_reject/denied:证据边界完整——哪怕仅展示也记录
    // (dry-run 下同样落此条目);reason 字段存原始 i18n key。
    // 事务提议附加 tx 判别三元组(intent/target/desired_state),链上可检索。
    await recordAuditFn(
      "copilot_reject",
      {
        command: proposal.command,
        args: proposal.args,
        risk: risk.level,
        reason: risk.reasonKey,
        ...(txIntent
          ? {
            tx_kind: txKind,
            target: txIntent.target,
            desired_state: txIntent.desiredState,
          }
          : {}),
      },
      "denied",
    );
    return 0;
  }

  // 步骤 2.5:verbose 先打印翻译结果(在任何可能触发 I/O 的动作之前)。
  // recordAudit 可能 resolveAuditPath 后 fallback $HOME 并弹 deno 权限窗——
  // 用户先看翻译结果再决定 Ctrl-C,无须对权限盲授权。
  // dryRun 路径有自己的 [dry-run] 预览,这里跳过避免双打印。
  if (verbose && !dryRun) {
    await io.writeStdout(t("verbose.preview", proposedStr) + "\n");
    if (proposal.explanation) {
      await io.writeStdout(t("verbose.explanation", proposal.explanation) + "\n");
    }
  }

  // 步骤 2.7:审计翻译事件。挪到 verbose 之后,确保用户在看到任何可能弹窗
  // 之前先看到翻译结果。审计哈希链对 args 序列化整体摘要,新增 key 天然兼容。
  await recordAuditFn(
    "copilot_translate",
    {
      query,
      round: 0,
      risk_level: risk.level,
      ...(txIntent ? { tx_kind: txKind } : {}),
    },
    "success",
  );

  if (dryRun) {
    await io.writeStdout(t("dryrun.would_execute", proposedStr) + "\n");
    if (verbose && proposal.explanation) {
      await io.writeStdout(t("dryrun.explanation", proposal.explanation) + "\n");
    }
    await recordAuditFn(
      "copilot_confirm",
      {
        command: proposal.command,
        args: proposal.args,
        mode: "dry-run",
        risk_level: "safe",
        // dry-run 对事务同样零副作用:不开账(begin)、不 propose、不 apply
        ...(txIntent ? { tx_kind: txKind } : {}),
      },
      "success",
    );
    return 0;
  }

  // 步骤 4:确认策略。TTY 默认 y/n,-y 跳过;非 TTY(管道/脚本)无 stdin 可读,
  // 仅沙箱白名单只读诊断运行,其余展示,与 -i 无关。-i 是冗余 alias(= 不传
  // -y),-y 是唯一跳过 y/n 的方式(给 CI/管道);-i + 非 TTY 已在 runCopilot
  // 入口报错拦截,不会到达这里。
  const requireConfirmation = isTerminal && !yes;

  // 步骤 4.5:safe 事务(tx_propose / tx_apply started|stopped)分流到事务通道
  // (begin→propose→preview→y/n→apply);caution/danger 已在展示分流处返回。
  if (isTxChannel && txIntent) {
    return await runTxTurn(ctx, proposal, txIntent);
  }

  if (!requireConfirmation) {
    // 沙箱执行路径(仅 L0 白名单只读诊断可达)
    await recordAuditFn(
      "copilot_confirm",
      {
        command: proposal.command,
        args: proposal.args,
        // 非 requireConfirmation 路径都是系统自动跑、用户未在 y/n 中确认;
        // requireConfirmation 的 "y" 路径在 y/n 循环里写 auto: false。
        auto: !requireConfirmation,
        // 能走到这里必为 L0(safe + 白名单内);risk_level 供审计链检索
        risk_level: "safe",
      },
      "success",
    );
    const execResult = await execFn(proposal.command, proposal.args);
    if (execResult.stdout) {
      await io.writeStdout(execResult.stdout);
    }
    if (execResult.stderr) {
      await io.writeStderr(execResult.stderr);
    }
    return execResult.returncode;
  }

  // 设置多轮修订状态:基础提示词 + (非空时)KNOWN STATE 追加段,修订轮 LLM
  // 上下文与首轮 translate 一致(注入对称性)。
  const history: Array<{
    role: "system" | "user" | "assistant";
    content: string;
  }> = [
    {
      role: "system",
      content: stateBlock
        ? `${buildSystemPrompt(currentLocale())}\n\n${stateBlock}`
        : buildSystemPrompt(currentLocale()),
    },
    { role: "user", content: query },
    { role: "assistant", content: rawLlmOutput },
  ];
  let revisionRound = 0;

  while (true) {
    const proposedStr =
      proposal.args.length > 0
        ? `${proposal.command} ${proposal.args.join(" ")}`
        : proposal.command;

    const display =
      t("confirm.request", query) + "\n" +
      t("confirm.proposed", proposedStr) + "\n" +
      t("confirm.explanation", proposal.explanation) + "\n" +
      t("confirm.privacy", config.provider) + "\n";

    await io.writeStdout(display);

    const choiceRaw = await io.readLine(t("confirm.prompt"));
    if (choiceRaw === null) {
      // EOF (Ctrl-D) 视作退出
      await recordAuditFn("copilot_cancel", { query, proposal }, "denied");
      return 0;
    }

    const choice = choiceRaw.trim().toLowerCase();

    if (choice === "y" || choice === "yes") {
      await recordAuditFn(
        "copilot_confirm",
        {
          command: proposal.command,
          args: proposal.args,
          auto: false,
          // L0 沙箱执行前的用户显式确认;risk_level 恒为 safe(非 L0 已被展示路径拦截)
          risk_level: "safe",
        },
        "success",
      );

      const execResult = await execFn(proposal.command, proposal.args);
      if (execResult.stdout) {
        await io.writeStdout(execResult.stdout);
      }
      if (execResult.stderr) {
        await io.writeStderr(execResult.stderr);
      }
      return execResult.returncode;
    }

    if (choice === "e" || choice === "edit") {
      const editLine = await io.readLine(t("confirm.edit_prompt"));
      if (editLine === null) {
        await recordAuditFn("copilot_cancel", { query, proposal }, "denied");
        return 0;
      }

      const trimmed = editLine.trim();
      if (!trimmed) {
        continue;
      }

      const parts = trimmed.split(/\s+/);
      const newProposal: CommandProposal = {
        command: parts[0],
        args: parts.slice(1),
        explanation: `${proposal.explanation} (user edited)`,
      };

      try {
        validateProposal(newProposal);
      } catch (err: unknown) {
        const msg = err instanceof Error ? err.message : String(err);
        await io.writeStderr(t("error.policy_reject", msg) + "\n");
        await recordAuditFn(
          "copilot_reject",
          {
            query,
            proposal: newProposal,
            reason: msg,
          },
          "denied",
        );
        continue;
      }

      await recordAuditFn(
        "copilot_edit",
        {
          original: proposal,
          edited: newProposal,
        },
        "success",
      );

      proposal = newProposal;
      continue;
    }

    if (choice === "n" || choice === "no") {
      revisionRound++;
      if (revisionRound > 3) {
        await io.writeStderr(t("confirm.revision_limit") + "\n");
        await recordAuditFn(
          "copilot_reject",
          {
            query,
            reason: "revision limit reached",
          },
          "denied",
        );
        return 1;
      }

      const feedback = await io.readLine(t("confirm.feedback_prompt"));
      if (feedback === null) {
        await recordAuditFn("copilot_cancel", { query, proposal }, "denied");
        return 0;
      }

      let revisedRawOutput: string;
      try {
        revisedRawOutput = await reviseFn(history, feedback, configOverrides);
      } catch (err: unknown) {
        // revise 与 translate 同走 llm.ts callProvider,错误形状一致——
        // 共用 renderLLMError 渲染 + 同构 copilot_error 审计(带 error_kind)
        const rendered = renderLLMError(err);
        await io.writeStderr(rendered.msg + "\n");
        await recordAuditFn(
          "copilot_error",
          buildErrorAuditArgs(query, rendered),
          "error",
        );
        return 1;
      }

      let revisedProposal: CommandProposal;
      try {
        revisedProposal = parseProposal(revisedRawOutput);
      } catch (err: unknown) {
        const msg = err instanceof Error ? err.message : String(err);
        await io.writeStderr(t("error.policy_reject", msg) + "\n");
        await recordAuditFn(
          "copilot_reject",
          {
            query,
            rawOutput: revisedRawOutput,
            reason: msg,
          },
          "denied",
        );
        return 1;
      }

      try {
        validateProposal(revisedProposal);
      } catch (err: unknown) {
        const msg = err instanceof Error ? err.message : String(err);
        await io.writeStderr(t("error.policy_reject", msg) + "\n");
        await recordAuditFn(
          "copilot_reject",
          {
            query,
            proposal: revisedProposal,
            reason: msg,
          },
          "denied",
        );
        return 126;
      }

      history.push({ role: "user", content: feedback });
      history.push({ role: "assistant", content: revisedRawOutput });
      proposal = revisedProposal;
      continue;
    }

    if (choice === "q" || choice === "quit") {
      await recordAuditFn("copilot_cancel", { query, proposal }, "denied");
      return 0;
    }

    await io.writeStdout(t("confirm.invalid_choice") + "\n");
  }
}

export async function runCopilot(options?: CopilotOptions): Promise<number> {
  // i18n 初始化必须最先执行：之后所有 t() 调用才拿到本地化文案。
  // 注意不要在初始化前调用 t()，也不要把 parseArgs 提到它前面。
  await initI18n();

  const io = createIO(options);
  const rawArgs =
    options?.args ??
    (typeof Deno?.args !== "undefined"
      ? Deno.args
      : (globalThis as any).process?.argv?.slice(2) ?? []);

  const parsed = parseArgs(rawArgs);

  const yes = options?.yes ?? parsed.yes;
  const verbose = options?.verbose ?? parsed.verbose;
  const interactive = options?.interactive ?? parsed.interactive;
  const dryRun = options?.dryRun ?? parsed.dryRun;
  const provider = options?.provider ?? parsed.provider;
  const model = options?.model ?? parsed.model;
  const baseUrl = options?.baseUrl ?? parsed.baseUrl;
  const help = parsed.help;
  const version = parsed.version;

  const configOverrides: Partial<Config> = {
    provider: (provider === "openai" || provider === "anthropic") ? provider : undefined,
    model,
    baseUrl,
  };

  const translateFn = options?.translateFn ?? translate;
  const reviseFn = options?.reviseFn ?? revise;
  const execFn = options?.execFn ?? execAllowlisted;
  const recordAuditFn = options?.recordAuditFn ?? recordAudit;
  const readConfigFn = options?.readConfigFn ?? readConfig;
  const txBeginFn = options?.txBeginFn ?? defaultTxBegin;
  const txProposeFn = options?.txProposeFn ?? execTxPropose;
  const txPreviewFn = options?.txPreviewFn ?? execTxPreview;
  const txApplyFn = options?.txApplyFn ?? execTxApply;
  const readStateFn = options?.readStateFn ?? readStateSummary;

  if (help) {
    await io.writeStdout(getHelpMessage());
    return 0;
  }

  if (version) {
    await io.writeStdout(t("version.banner", VERSION) + "\n");
    return 0;
  }

  let query = (options?.query ?? parsed.query).trim();

  if (!io.isTerminal) {
    // 唯一被拒绝的组合：显式要求交互确认但 stdin 不是终端
    if (interactive) {
      await io.writeStderr(t("error.interactive_non_tty"));
      return 1;
    }

    if (!query && io.readStdinAll) {
      query = await io.readStdinAll();
    }

    if (!query) {
      return 0;
    }

    return await runQueryTurn({
      query,
      yes: true,
      verbose,
      interactive: false,
      dryRun,
      configOverrides,
      isTerminal: false,
      io,
      translateFn,
      reviseFn,
      execFn,
      recordAuditFn,
      readConfigFn,
      txBeginFn,
      txProposeFn,
      txPreviewFn,
      txApplyFn,
      readStateFn,
    });
  }

  if (query) {
    return await runQueryTurn({
      query,
      yes,
      verbose,
      interactive,
      dryRun,
      configOverrides,
      isTerminal: true,
      io,
      translateFn,
      reviseFn,
      execFn,
      recordAuditFn,
      readConfigFn,
      txBeginFn,
      txProposeFn,
      txPreviewFn,
      txApplyFn,
      readStateFn,
    });
  }

  while (true) {
    const line = await io.readLine(t("repl.prompt"));
    if (line === null) {
      await io.writeStdout("\n");
      return 0;
    }

    const trimmed = line.trim();
    if (trimmed === "exit" || trimmed === "quit") {
      return 0;
    }
    if (!trimmed) {
      continue;
    }

    // CLI 级别的 verbose/interactive/dryRun 标志对每条 REPL 查询生效。
    await runQueryTurn({
      query: trimmed,
      yes: false,
      verbose,
      interactive,
      dryRun,
      configOverrides,
      isTerminal: true,
      io,
      translateFn,
      reviseFn,
      execFn,
      recordAuditFn,
      readConfigFn,
      txBeginFn,
      txProposeFn,
      txPreviewFn,
      txApplyFn,
      readStateFn,
    });
  }
}

if (import.meta.main) {
  const code = await runCopilot();
  if (typeof (globalThis as any).Deno?.exit === "function") {
    (globalThis as any).Deno.exit(code);
  } else if (typeof process?.exit === "function") {
    process.exit(code);
  }
}
