/**
 * Daedalus OS Copilot CLI 编排器。
 *
 * 实现交互式和单次自然语言 CLI 编排：
 * 1. CLI 参数解析（--verbose, --interactive, --dry-run, --yes, --provider, --model, --base-url, --help, --version）
 * 2. 交互式终端的 REPL 模式，每个查询具有独立的修订计数器
 * 3. 建议先行:所有命令先展示 + 风险标注;交互终端下白名单内只读诊断经 y/n 确认后由沙箱运行,白名单外只展示由用户手动执行;非 TTY 仅白名单只读诊断运行
 * 4. 分步轮次循环：转换 -> 解析并验证 -> 确认/编辑/拒绝反馈循环 -> 执行
 * 5. 针对每个生命周期事件严格执行加密哈希链审计日志记录
 */

import {
  parseProposal,
  classifyProposal,
  L0_WHITELIST,
  validateProposal,
  buildSystemPrompt,
  type CommandProposal,
  type RiskAssessment,
} from "./policy.ts";
import { recordAudit, type AuditOutcome } from "./audit.ts";
import { readConfig, translate, revise, type Config } from "./llm.ts";
import { execAllowlisted, type ExecResult } from "./exec.ts";
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
}

/**
 * 将 CLI 参数数组解析为结构化标志和位置查询。
 */
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

/**
 * 检查标准输入是否连接到交互式终端。
 */
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

/**
 * 跨 Deno / Node 环境的标准输入流回退按行读取器。
 */
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
 * 当通过管道输入时，从标准输入读取所有内容的回退方法。
 *
 * Deno 2 已移除 `Deno.readAll`，管道读取必须使用
 * `Deno.stdin.readable` 的异步迭代（for await）逐块累积。
 * 任何读取异常都返回空串，由调用方处理空查询场景。
 * 导出仅用于单元测试注入 stdin.readable 场景。
 */
export async function defaultReadStdinAll(): Promise<string> {
  const deno = (globalThis as any).Deno;
  // 优先走原生 Deno 路径：异步迭代 stdin.readable 读取全部字节
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
  // 非 Deno 环境（如 Node）下的兜底：同步读取 fd 0
  try {
    const fs = require("fs");
    return fs.readFileSync(0, "utf-8").trim();
  } catch {
    return "";
  }
}

/**
 * 初始化默认输入输出处理器。
 */
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

/**
 * 返回格式化的 CLI 使用说明。
 * 全部文案经 t() 走 i18n，逐段拼接保证 locale 切换后仍是连贯文本。
 */
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
 * 消费 llm.ts 的结构化 LLM 错误(W1),渲染为本地化可操作文案。
 *
 * 上游形状(决策 7:Error + 附加属性,非子类):
 * `Object.assign(new Error(originalMsg), { kind, fields })`,
 * kind ∈ timeout|http|network|config,fields 携带 endpoint/timeoutMs/status/body/err。
 * 本函数用类型守卫读 kind/fields——无 kind(意外异常)一律走兜底:
 * t("error.translate", 原始 message) 原样透传 + error_kind="unknown"。
 * timeout 文案占位 {1} 为秒:Math.round(timeoutMs/1000)(1000ms → "1" 而非 "0.001")。
 */
function renderLLMError(err: unknown): RenderedLLMError {
  const originalMsg = err instanceof Error ? err.message : String(err);
  // 类型守卫:kind 必须是字符串、fields 必须是对象才认定为结构化错误
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
      // 无 kind / 未知 kind:意外异常兜底,原始 message 经 error.translate("{0}") 原样透传
      return {
        msg: t("error.translate", originalMsg),
        kind: ERROR_KIND_UNKNOWN,
        fields,
      };
  }
}

/**
 * 从渲染结果构造 copilot_error 审计 args:核心三键恒定,
 * fields 明细条件展开(无值即无键)——保持 args 为可 JSON 序列化的普通对象,
 * 不在审计链里留 null/undefined 噪音。
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
 * 执行单次查询轮次：转换 -> 解析并验证 -> 可选 verbose/dryRun -> 可选 interactive 确认 -> 执行。
 */
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
  } = ctx;

  // 解析配置以获取提供商标识和设置
  let config: Config;
  try {
    config = readConfigFn(configOverrides);
  } catch (err: unknown) {
    const msg = err instanceof Error ? err.message : String(err);
    await io.writeStderr(t("error.config", msg) + "\n");
    await recordAuditFn("copilot_error", { query, error: msg }, "error");
    return 1;
  }

  // 步骤 1：将查询转换为 LLM 提议
  let rawLlmOutput: string;
  try {
    rawLlmOutput = await translateFn(query, configOverrides);
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

  // 步骤 2：解析并预验证提议
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

  // 步骤 2.4：本地静态风险分级（决策 2：LLM 不参与风险自标注）。
  // L0 = safe 且命中 15 命令白名单 → 唯一可能进入沙箱执行的分级;
  // safe-outside / caution / danger 一律走展示路径,永不执行。
  let risk: RiskAssessment;
  try {
    risk = classifyProposal(proposal);
  } catch (err: unknown) {
    // classifyProposal 的入参防御分支:parseProposal 已保证 schema,理论不可达
    const msg = err instanceof Error ? err.message : String(err);
    await io.writeStderr(t("error.policy_reject", msg) + "\n");
    await recordAuditFn(
      "copilot_reject",
      { query, proposal, reason: msg },
      "denied",
    );
    return 126;
  }

  const proposedStr = proposal.args.length > 0
    ? `${proposal.command} ${proposal.args.join(" ")}`
    : proposal.command;

  // 展示路径:caution / danger / safe(白名单外)。
  // 架构上不可执行:daedalus-shell 拒白名单外命令,这里也不给 y 选项——模式分离的物理保障。
  // (danger 的 reasonKey 必非空;safe-outside / caution 亦带 i18n reasonKey)
  if (risk.level !== "safe" || !L0_WHITELIST.has(proposal.command)) {
    if (risk.level === "safe") {
      // 白名单外 safe:✓ 标签 + 手动执行提示(pivot 核心场景,如 git --version)
      await io.writeStdout(t("risk.banner.safe") + "\n");
    } else if (risk.level === "caution") {
      // caution:⚠ 标签,改系统状态但通常可回滚
      await io.writeStdout(t("risk.banner.caution") + "\n");
    } else {
      // danger:🚨 标签 + 独立原因行。原因行仅在 reasonKey 非空且非
      // outside_sandbox / caution_command 时渲染(pattern key 才有具体后果文案;
      // caution_command 的解释已在 banner 里,分类器也不会对 danger 产出这两个 key,防御性保留)。
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
    // (F3 验收:dry-run 下同样落此条目);reason 字段存原始 i18n key。
    await recordAuditFn(
      "copilot_reject",
      { command: proposal.command, args: proposal.args, risk: risk.level, reason: risk.reasonKey },
      "denied",
    );
    return 0;
  }

  // 步骤 2.5：verbose 模式先打印翻译结果(在任何可能触发 I/O 的动作之前)。
  // recordAudit 内部会走 audit.ts:resolveAuditPath,主系统目录不可写时
  // 会 fallback 到 $HOME/.local/share/daedalus 并 Deno.mkdirSync;若
  // 路径不在 allow list 会弹 deno 权限窗。把 verbose 挪到这里,用户在
  // 看到翻译结果后才决定要不要 Ctrl-C 取消,无须先对权限盲授权。
  // dryRun 路径有自己的 [dry-run] 预览,这里跳过避免双打印。
  if (verbose && !dryRun) {
    await io.writeStdout(t("verbose.preview", proposedStr) + "\n");
    if (proposal.explanation) {
      await io.writeStdout(t("verbose.explanation", proposal.explanation) + "\n");
    }
  }

  // 步骤 2.7：审计翻译事件。挪到 verbose 之后,确保用户在看到任何
  // 可能弹窗之前先看到翻译结果。risk_level 字段为 QQ pivot 新增
  // (决策 11);审计哈希链对 args 序列化整体摘要,新增 key 天然兼容。
  await recordAuditFn(
    "copilot_translate",
    { query, round: 0, risk_level: risk.level },
    "success",
  );

  // 步骤 3：dry-run 模式 - 仅显示命令，不执行
  if (dryRun) {
    await io.writeStdout(t("dryrun.would_execute", proposedStr) + "\n");
    if (verbose && proposal.explanation) {
      await io.writeStdout(t("dryrun.explanation", proposal.explanation) + "\n");
    }
    await recordAuditFn(
      "copilot_confirm",
      { command: proposal.command, args: proposal.args, mode: "dry-run", risk_level: "safe" },
      "success",
    );
    return 0;
  }

  // 步骤 4：决定是否需要交互确认
  // TTY 模式(REPL / one-shot in 终端):默认 y/n 确认,除非 -y 跳过。
  // 非 TTY 模式(管道/脚本):无 stdin 可读;仅沙箱白名单内只读诊断运行,其余只展示,与 -i 无关。
  // -i 仍接受为冗余 alias(语义等价于"不传 -y",即"我要 y/n");-y 是
  // 唯一跳过 y/n 的方式,给 CI / 管道用。-i + 非 TTY 已在 runCopilot
  // 入口处报错拦截,不会到达这里。
  const requireConfirmation = isTerminal && !yes;

  if (!requireConfirmation) {
    // 沙箱执行路径(仅 L0 白名单只读诊断可达)
    await recordAuditFn(
      "copilot_confirm",
      {
        command: proposal.command,
        args: proposal.args,
        // 非 requireConfirmation 路径(TTY -y 跳过,或非 TTY 无 stdin)
        // 都属"系统自动跑,用户未在 y/n 循环中确认";requireConfirmation
        // 为 true 走 y/n 循环,"y" 路径在那里写 auto: false。
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

  // 走到这里说明 requireConfirmation === true（隐含 isTerminal === true）
  // 设置多轮修订状态
  const history: Array<{
    role: "system" | "user" | "assistant";
    content: string;
  }> = [
    { role: "system", content: buildSystemPrompt(currentLocale()) },
    { role: "user", content: query },
    { role: "assistant", content: rawLlmOutput },
  ];
  let revisionRound = 0;

  // 步骤 4 与 5：交互式确认与反馈循环
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

    // 选项：[y]es -> 确认并执行
    if (choice === "y" || choice === "yes") {
      await recordAuditFn(
        "copilot_confirm",
        {
          command: proposal.command,
          args: proposal.args,
          auto: false,
          // L0 沙箱执行前的用户显式确认;risk_level 恒为 safe
          // (非 L0 提议早在本轮之前就被展示路径拦截,不会进入 y/n 循环)
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

    // 选项：[e]dit -> 用户直接编辑并进行本地重新验证（不调用 LLM）
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

    // 选项：[n]o -> 反馈轮次（最多 3 次修订）
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

    // 选项：[q]uit -> 取消并以状态码 0 退出
    if (choice === "q" || choice === "quit") {
      await recordAuditFn("copilot_cancel", { query, proposal }, "denied");
      return 0;
    }

    await io.writeStdout(t("confirm.invalid_choice") + "\n");
  }
}

/**
 * 主 Copilot 运行器入口点。
 */
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

  // 合并直接传入的选项覆盖
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

  if (help) {
    await io.writeStdout(getHelpMessage());
    return 0;
  }

  if (version) {
    await io.writeStdout(t("version.banner", VERSION) + "\n");
    return 0;
  }

  let query = (options?.query ?? parsed.query).trim();

  // 非 TTY（管道/脚本）模式:仅白名单只读诊断运行,其余展示
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
    });
  }

  // 单次查询模式（CLI 提供了查询参数，终端环境）
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
    });
  }

  // 交互式 REPL 模式（CLI 未提供查询参数且标准输入为终端）
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

    // 运行独立的查询轮次，并使用全新的修订计数器
    // CLI 级别的 verbose/interactive/dryRun 标志对每条 REPL 查询生效
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
    });
  }
}

// 入口点执行
if (import.meta.main) {
  const code = await runCopilot();
  if (typeof (globalThis as any).Deno?.exit === "function") {
    (globalThis as any).Deno.exit(code);
  } else if (typeof process?.exit === "function") {
    process.exit(code);
  }
}
