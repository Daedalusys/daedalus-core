/**
 * Daedalus OS Copilot CLI 编排器。
 *
 * 实现交互式和单次自然语言 CLI 编排：
 * 1. CLI 参数解析（--verbose, --interactive, --dry-run, --yes, --provider, --model, --base-url, --help, --version）
 * 2. 交互式终端的 REPL 模式，每个查询具有独立的修订计数器
 * 3. 默认自动执行（无需确认）；仅 -i/--interactive 强制 y/e/n/q 确认循环（要求 TTY）
 * 4. 分步轮次循环：转换 -> 解析并验证 -> 确认/编辑/拒绝反馈循环 -> 执行
 * 5. 针对每个生命周期事件严格执行加密哈希链审计日志记录
 */

import {
  parseProposal,
  validateProposal,
  buildSystemPrompt,
  type CommandProposal,
} from "./policy.ts";
import { recordAudit, type AuditOutcome } from "./audit.ts";
import { readConfig, translate, revise, type Config } from "./llm.ts";
import { execAllowlisted, type ExecResult } from "./exec.ts";

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
    verbose: false,
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
      result.verbose = true;
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
      const line = (globalThis as any).prompt(promptText ?? "Proceed? [y]es / [e]dit / [n]o / [q]uit: ");
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
 */
export function getHelpMessage(): string {
  return `Daedalus OS Copilot - AI-Native Command Assistant

Usage:
  daedalus [options] [query...]

默认行为：直接执行。LLM 翻译后的命令会静默运行，仅输出执行结果。
如有顾虑，使用 -v 查看翻译过程，或使用 -i 强制交互确认。

Options:
  -v, --verbose         Show LLM translation (command + explanation) before execution
  -i, --interactive     Force interactive confirmation (y/n prompt)
  -y, --yes             Alias for default (auto-execute) — kept for backward compat
  --dry-run             Show command that would be executed, but do NOT run it
  --provider <name>     LLM provider override (openai | anthropic)
  --model <name>        LLM model override
  --base-url <url>      LLM base URL override
  -V, --version         Show version information
  -h, --help            Show this help message

Examples:
  daedalus check memory
  daedalus -v list network interfaces
  daedalus --dry-run delete temp files
  daedalus -i remove old logs
  daedalus (starts interactive REPL mode)
`;
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
    await io.writeStderr(`Configuration error: ${msg}\n`);
    await recordAuditFn("copilot_error", { query, error: msg }, "error");
    return 1;
  }

  // 步骤 1：将查询转换为 LLM 提议
  let rawLlmOutput: string;
  try {
    rawLlmOutput = await translateFn(query, configOverrides);
  } catch (err: unknown) {
    const msg = err instanceof Error ? err.message : String(err);
    await io.writeStderr(`${msg}\n`);
    await recordAuditFn("copilot_error", { query, error: msg }, "error");
    return 1;
  }

  await recordAuditFn(
    "copilot_translate",
    { query, round: 0 },
    "success",
  );

  // 步骤 2：解析并预验证提议
  let proposal: CommandProposal;
  try {
    proposal = parseProposal(rawLlmOutput);
  } catch (err: unknown) {
    const msg = err instanceof Error ? err.message : String(err);
    await io.writeStderr(`Security policy rejection: ${msg}\n`);
    await recordAuditFn(
      "copilot_reject",
      { query, rawOutput: rawLlmOutput, reason: msg },
      "denied",
    );
    return 1;
  }

  try {
    validateProposal(proposal);
  } catch (err: unknown) {
    const msg = err instanceof Error ? err.message : String(err);
    await io.writeStderr(`Security policy rejection: ${msg}\n`);
    await recordAuditFn(
      "copilot_reject",
      { query, proposal, reason: msg },
      "denied",
    );
    return 126;
  }

  // 步骤 3：dry-run 模式 - 仅显示命令，不执行
  if (dryRun) {
    const proposedStr = proposal.args.length > 0
      ? `${proposal.command} ${proposal.args.join(" ")}`
      : proposal.command;
    await io.writeStdout(`[dry-run] Would execute: ${proposedStr}\n`);
    if (verbose && proposal.explanation) {
      await io.writeStdout(`  (explanation: ${proposal.explanation})\n`);
    }
    await recordAuditFn(
      "copilot_confirm",
      { command: proposal.command, args: proposal.args, mode: "dry-run" },
      "success",
    );
    return 0;
  }

  // 步骤 4：verbose 模式 - 显示翻译结果（但不弹交互确认）
  if (verbose) {
    const proposedStr = proposal.args.length > 0
      ? `${proposal.command} ${proposal.args.join(" ")}`
      : proposal.command;
    await io.writeStdout(`→ ${proposedStr}\n`);
    if (proposal.explanation) {
      await io.writeStdout(`  ${proposal.explanation}\n`);
    }
  }

  // 步骤 5：决定是否需要交互确认
  // 默认（无 -i）：直接自动执行，不弹出 y/n 提示
  // -i + isTerminal：进入 y/e/n/q 交互确认循环
  // -i + 非 TTY：已在 runCopilot 入口处报错拦截，不会到达这里
  const requireConfirmation = interactive && isTerminal;

  if (!requireConfirmation) {
    // 自动执行路径（默认行为）
    await recordAuditFn(
      "copilot_confirm",
      {
        command: proposal.command,
        args: proposal.args,
        auto: !interactive,
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
    { role: "system", content: buildSystemPrompt() },
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
      `Request: ${query}\n` +
      `Proposed: ${proposedStr}\n` +
      `Explanation: ${proposal.explanation}\n` +
      `[privacy] This request and proposal were sent to ${config.provider} (cloud LLM).\n`;

    await io.writeStdout(display);

    const choiceRaw = await io.readLine(
      "Proceed? [y]es / [e]dit / [n]o (give feedback) / [q]uit: ",
    );
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
      const editLine = await io.readLine("Edit command [command arg1 arg2...]: ");
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
        await io.writeStderr(`Security policy rejection: ${msg}\n`);
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
        await io.writeStderr("Revision limit reached (3).\n");
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

      const feedback = await io.readLine("Feedback for revision: ");
      if (feedback === null) {
        await recordAuditFn("copilot_cancel", { query, proposal }, "denied");
        return 0;
      }

      let revisedRawOutput: string;
      try {
        revisedRawOutput = await reviseFn(history, feedback, configOverrides);
      } catch (err: unknown) {
        const msg = err instanceof Error ? err.message : String(err);
        await io.writeStderr(`${msg}\n`);
        await recordAuditFn("copilot_error", { query, error: msg }, "error");
        return 1;
      }

      let revisedProposal: CommandProposal;
      try {
        revisedProposal = parseProposal(revisedRawOutput);
      } catch (err: unknown) {
        const msg = err instanceof Error ? err.message : String(err);
        await io.writeStderr(`Security policy rejection: ${msg}\n`);
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
        await io.writeStderr(`Security policy rejection: ${msg}\n`);
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

    await io.writeStdout("Invalid choice. Please choose [y]es, [e]dit, [n]o, or [q]uit.\n");
  }
}

/**
 * 主 Copilot 运行器入口点。
 */
export async function runCopilot(options?: CopilotOptions): Promise<number> {
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
    await io.writeStdout(`daedalus-copilot ${VERSION}\n`);
    return 0;
  }

  let query = (options?.query ?? parsed.query).trim();

  // 非 TTY（管道/脚本）模式：默认直接自动执行，无需 --yes
  if (!io.isTerminal) {
    // 唯一被拒绝的组合：显式要求交互确认但 stdin 不是终端
    if (interactive) {
      await io.writeStderr(
        "Interactive confirmation requested (-i) but stdin is not a TTY. Drop -i to auto-execute.\n",
      );
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
    const line = await io.readLine("daedalus> ");
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
