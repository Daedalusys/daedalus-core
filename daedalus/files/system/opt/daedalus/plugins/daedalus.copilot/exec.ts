/**
 * Daedalus OS Copilot 命令执行桥接器。
 *
 * 启动沙箱化的 daedalus-shell Go MCP 服务器二进制（stdio JSON-RPC），
 * 并按行分隔执行 JSON-RPC 2.0 握手以调用 `shell_exec` 能力工具。
 *
 * 强制执行 40 秒看门狗定时器，将子进程的 stderr 与 stdout 分离，
 * 通过 SIGINT 监听器处理进程清理，并对 JSON-RPC 结果进行二次解析。
 */

export interface ExecResult {
  stdout: string;
  stderr: string;
  returncode: number;
  error?: string | null;
}

// 跟踪活动子进程以便在信号终止时进行安全清理
const activeProcesses = new Set<{ kill(signo?: string): void }>();

let signalHandlerRegistered = false;

/**
 * 在 Deno 或 Node/Bun 运行时中注册一次 SIGINT 处理器。
 */
function ensureSignalHandlerRegistered(): void {
  if (signalHandlerRegistered) {
    return;
  }
  signalHandlerRegistered = true;

  const handleSigint = () => {
    for (const proc of activeProcesses) {
      try {
        proc.kill("SIGKILL");
      } catch {
        // 子进程可能已经退出
      }
    }
    activeProcesses.clear();

    if (typeof (globalThis as any).Deno?.exit === "function") {
      (globalThis as any).Deno.exit(130);
    } else if (typeof process?.exit === "function") {
      process.exit(130);
    }
  };

  if (typeof (globalThis as any).Deno?.addSignalListener === "function") {
    try {
      (globalThis as any).Deno.addSignalListener("SIGINT", handleSigint);
    } catch {
      // 若 Deno 环境不支持信号监听器则回退
      if (typeof process?.on === "function") {
        process.on("SIGINT", handleSigint);
      }
    }
  } else if (typeof process?.on === "function") {
    process.on("SIGINT", handleSigint);
  }
}

/**
 * 探测文件/路径是否可访问（stat 成功即视为存在）。
 */
function pathExists(target: string): boolean {
  try {
    (globalThis as any).Deno.statSync(target);
    return true;
  } catch {
    // 文件不存在或不可访问
    return false;
  }
}

/**
 * 读取看门狗超时毫秒数：DAEDALUS_WATCHDOG_TIMEOUT_MS 环境变量覆写 → 缺省 40000。
 * execAllowlisted（MCP 桥）与 tx CLI 调用层（execTx*）共用同一旋钮，
 * 保证两条执行通道的超时语义逐字节一致（rc 124 / SIGKILL / 同款提示文案）。
 */
function resolveWatchdogTimeoutMs(): number {
  return Number(
    (typeof (globalThis as any).Deno?.env?.get === "function"
      ? (globalThis as any).Deno.env.get("DAEDALUS_WATCHDOG_TIMEOUT_MS")
      : process.env.DAEDALUS_WATCHDOG_TIMEOUT_MS) ?? 40000,
  );
}

/**
 * 解析 Go 版 daedalus-shell MCP 服务器（stdio JSON-RPC）二进制路径。
 * 解析顺序：DAEDALUS_SHELL_BIN 环境变量 → 生产默认 /usr/local/bin/daedalus-shell →
 *   开发态仓库构建产物 daedalus/core/bin/daedalus-shell（含向上回溯最多 10 层父目录）。
 * 全部探测失败时回退到生产路径（让用户看到友好错误）。
 */
function resolveShellBinary(): string {
  const envPath =
    typeof (globalThis as any).Deno?.env?.get === "function"
      ? (globalThis as any).Deno.env.get("DAEDALUS_SHELL_BIN")
      : process.env.DAEDALUS_SHELL_BIN;
  if (envPath) {
    return envPath;
  }

  const productionPath = "/usr/local/bin/daedalus-shell";
  if (pathExists(productionPath)) {
    return productionPath;
  }

  const devCandidates = [
    "daedalus/core/bin/daedalus-shell",
    "../daedalus/core/bin/daedalus-shell",
  ];
  for (const rel of devCandidates) {
    if (pathExists(rel)) {
      return rel;
    }
  }

  // 向上回溯最多 10 层父目录查找仓库内构建产物
  let cwd = (globalThis as any).Deno.cwd();
  for (let i = 0; i < 10; i++) {
    const tryPath = `${cwd}/daedalus/core/bin/daedalus-shell`;
    if (pathExists(tryPath)) {
      return tryPath;
    }
    const parent = cwd.replace(/\/[^/]+\/?$/, "");
    if (parent === cwd) break;
    cwd = parent;
  }
  return productionPath;
}

/**
 * 解析 Go 版 daedalus-tx 事务原语 CLI（纯命令行, 非 MCP 服务器）二进制路径。
 * 与 resolveShellBinary 逐项镜像（计划 todo 26）：
 *   DAEDALUS_TX_BIN 环境变量 → 生产默认 /usr/local/bin/daedalus-tx →
 *   开发态仓库构建产物 daedalus/core/bin/daedalus-tx（含向上回溯最多 10 层父目录）。
 * 全部探测失败时回退到生产路径（让用户看到友好错误）。
 * 注：manifest `--allow-run` 已放行 /usr/local/bin/daedalus-tx（todo 25）；
 * dev 态走 DAEDALUS_TX_BIN 覆写 + 向上回溯是钉死的合规开发流（todo 16 v1 执行模型：
 * daedalus-tx 是调用用户自己的进程, 无 systemd 单元）。
 */
function resolveTxBinary(): string {
  const envPath = typeof (globalThis as any).Deno?.env?.get === "function"
    ? (globalThis as any).Deno.env.get("DAEDALUS_TX_BIN")
    : process.env.DAEDALUS_TX_BIN;
  if (envPath) {
    return envPath;
  }

  const productionPath = "/usr/local/bin/daedalus-tx";
  if (pathExists(productionPath)) {
    return productionPath;
  }

  const devCandidates = [
    "daedalus/core/bin/daedalus-tx",
    "../daedalus/core/bin/daedalus-tx",
  ];
  for (const rel of devCandidates) {
    if (pathExists(rel)) {
      return rel;
    }
  }

  // 向上回溯最多 10 层父目录查找仓库内构建产物
  let cwd = (globalThis as any).Deno.cwd();
  for (let i = 0; i < 10; i++) {
    const tryPath = `${cwd}/daedalus/core/bin/daedalus-tx`;
    if (pathExists(tryPath)) {
      return tryPath;
    }
    const parent = cwd.replace(/\/[^/]+\/?$/, "");
    if (parent === cwd) break;
    cwd = parent;
  }
  return productionPath;
}

/**
 * 通过派生的 daedalus-shell Go MCP 服务器执行已列入白名单的命令。
 */
export async function execAllowlisted(
  command: string,
  args: string[] = [],
): Promise<ExecResult> {
  ensureSignalHandlerRegistered();

  const shellBinary = resolveShellBinary();

  const CommandConstructor = (globalThis as any).Deno?.Command;
  if (!CommandConstructor) {
    throw new Error("Deno.Command is not available in the current runtime environment");
  }

  // Go 二进制为自包含静态可执行文件，stdio MCP 协议握手保持不变；
  // 运行时权限沙箱由 systemd 单元（DynamicUser/ProtectSystem/Landlock/seccomp）承担。
  const childCmd = new CommandConstructor(shellBinary, {
    args: [],
    stdin: "piped",
    stdout: "piped",
    stderr: "piped",
  });

  let childProcess: any;
  try {
    childProcess = childCmd.spawn();
  } catch (err: unknown) {
    const msg = err instanceof Error ? err.message : String(err);
    return {
      stdout: "",
      stderr: `Failed to spawn shell MCP server: ${msg}`,
      returncode: 1,
      error: msg,
    };
  }

  activeProcesses.add(childProcess);

  const encoder = new TextEncoder();
  const decoder = new TextDecoder();

  // 异步将子进程 stderr 管道传输到单独的缓冲区/流中
  let childStderrText = "";
  const stderrPromise = (async () => {
    try {
      if (childProcess.stderr && typeof childProcess.stderr.getReader === "function") {
        const reader = childProcess.stderr.getReader();
        while (true) {
          const { value, done } = await reader.read();
          if (done) break;
          if (value) {
            childStderrText += decoder.decode(value, { stream: true });
          }
        }
      } else if (childProcess.stderr && typeof childProcess.stderr.read === "function") {
        const buf = new Uint8Array(4096);
        while (true) {
          const n = await childProcess.stderr.read(buf);
          if (n === null || n === 0) break;
          childStderrText += decoder.decode(buf.subarray(0, n), { stream: true });
        }
      }
    } catch {
      // 忽略 stderr 流传输错误
    }
  })();

  // 标准输入输出 JSON-RPC 通信处理器
  let timeoutId: any;
  let isTimedOut = false;

  // 看门狗超时与 tx 调用层共用同一解析（默认 40000ms, env 覆写 DAEDALUS_WATCHDOG_TIMEOUT_MS）
  const timeoutMs = resolveWatchdogTimeoutMs();

  const watchdogPromise = new Promise<ExecResult>((resolve) => {
    timeoutId = setTimeout(() => {
      isTimedOut = true;
      try {
        childProcess.kill("SIGKILL");
      } catch {
        // 如果已经终止则忽略
      }
      resolve({
        stdout: "",
        stderr: "Timeout: command execution exceeded 40s",
        returncode: 124,
        error: "copilot exec timeout",
      });
    }, timeoutMs);
  });

  const rpcPromise = (async (): Promise<ExecResult> => {
    try {
      const stdinWriter = childProcess.stdin?.getWriter
        ? childProcess.stdin.getWriter()
        : null;

      const writeLine = async (msg: Record<string, unknown>) => {
        const line = encoder.encode(JSON.stringify(msg) + "\n");
        if (stdinWriter) {
          await stdinWriter.write(line);
        } else if (childProcess.stdin?.write) {
          await childProcess.stdin.write(line);
        }
      };

      // 用于从子进程 stdout 读取 JSON-RPC 行的辅助函数
      let stdoutBuffer = "";
      const stdoutReader = childProcess.stdout?.getReader
        ? childProcess.stdout.getReader()
        : null;

      const readNextJsonLine = async (): Promise<Record<string, unknown>> => {
        while (true) {
          const newlineIdx = stdoutBuffer.indexOf("\n");
          if (newlineIdx !== -1) {
            const rawLine = stdoutBuffer.slice(0, newlineIdx).trim();
            stdoutBuffer = stdoutBuffer.slice(newlineIdx + 1);
            if (rawLine.length > 0) {
              return JSON.parse(rawLine);
            }
            continue;
          }

          let chunk: Uint8Array | null = null;
          if (stdoutReader) {
            const { value, done } = await stdoutReader.read();
            if (done) {
              break;
            }
            chunk = value;
          } else if (childProcess.stdout?.read) {
            const buf = new Uint8Array(4096);
            const n = await childProcess.stdout.read(buf);
            if (n === null || n === 0) {
              break;
            }
            chunk = buf.subarray(0, n);
          } else {
            break;
          }

          if (chunk) {
            stdoutBuffer += decoder.decode(chunk, { stream: true });
          }
        }

        const remaining = stdoutBuffer.trim();
        if (remaining.length > 0) {
          stdoutBuffer = "";
          return JSON.parse(remaining);
        }

        throw new Error("Unexpected EOF from shell MCP server stdout");
      };

      // 步骤 1：发送 initialize 请求（id: 1）
      await writeLine({
        jsonrpc: "2.0",
        id: 1,
        method: "initialize",
        params: {
          protocolVersion: "2024-11-05",
          capabilities: {},
          clientInfo: {
            name: "daedalus-copilot",
            version: "1.0",
          },
        },
      });

      // 步骤 2：读取 id 1 的 initialize 响应
      const initResp = await readNextJsonLine();
      if (initResp.id !== 1 || initResp.error) {
        throw new Error(
          `MCP initialize failed: ${
            initResp.error
              ? JSON.stringify(initResp.error)
              : `unexpected id ${initResp.id}`
          }`,
        );
      }

      // 步骤 3：发送 notifications/initialized
      await writeLine({
        jsonrpc: "2.0",
        method: "notifications/initialized",
      });

      // 步骤 4：发送 tools/call 请求（id: 2）
      await writeLine({
        jsonrpc: "2.0",
        id: 2,
        method: "tools/call",
        params: {
          name: "shell_exec",
          arguments: {
            command,
            args,
          },
        },
      });

      // 步骤 5：读取 id 2 的 tools/call 响应
      const callResp = await readNextJsonLine();
      if (callResp.id !== 2) {
        throw new Error(`Expected response id 2, got ${callResp.id}`);
      }

      if (callResp.error) {
        const errObj = callResp.error as Record<string, unknown>;
        return {
          stdout: "",
          stderr: String(errObj.message || "MCP tools/call error"),
          returncode: typeof errObj.code === "number" ? errObj.code : 1,
          error: String(errObj.message || "MCP tools/call error"),
        };
      }

      const result = callResp.result as Record<string, unknown>;
      const contentList = (result?.content as Array<{ type: string; text: string }>) || [];
      const firstText = contentList[0]?.text;

      if (typeof firstText !== "string") {
        throw new Error("MCP response missing result.content[0].text payload");
      }

      // 二次解析 result.content[0].text
      const innerResult = JSON.parse(firstText) as {
        stdout?: string;
        stderr?: string;
        returncode?: number;
        error?: string | null;
      };

      return {
        stdout: typeof innerResult.stdout === "string" ? innerResult.stdout : "",
        stderr: typeof innerResult.stderr === "string" ? innerResult.stderr : "",
        returncode:
          typeof innerResult.returncode === "number" ? innerResult.returncode : 0,
        error: innerResult.error ?? null,
      };
    } catch (rpcErr: unknown) {
      if (isTimedOut) {
        return {
          stdout: "",
          stderr: "Timeout: command execution exceeded 40s",
          returncode: 124,
          error: "copilot exec timeout",
        };
      }
      const msg = rpcErr instanceof Error ? rpcErr.message : String(rpcErr);
      return {
        stdout: "",
        stderr: msg,
        returncode: 1,
        error: msg,
      };
    } finally {
      // 若未被看门狗终止，则清理子进程
      if (!isTimedOut) {
        try {
          childProcess.kill("SIGTERM");
        } catch {
          // 子进程可能已经终止
        }
      }
    }
  })();

  try {
    const result = await Promise.race([rpcPromise, watchdogPromise]);
    return result;
  } finally {
    if (timeoutId) {
      clearTimeout(timeoutId);
    }
    activeProcesses.delete(childProcess);
    await stderrPromise.catch(() => {});
  }
}

// ===========================================================================
// daedalus-tx 事务原语调用层（计划 todo 26）。
//
// ★ 与 execAllowlisted 的本质区别: daedalus-tx 是**用户态一次性 CLI**(todo 15/16),
// 不是 stdio MCP 服务器 —— 每次调用 spawn argv、读 stdout **恰一份**机器可读
// JSON 文档(todo 15 stdout 契约), 错误也走 {"error":...} + 非零退出码
// (1 运行期 / 2 用法)。本层直接 JSON.parse CLI stdout, **不经 MCP 的
// result.content[].text 二次包裹** —— 没有 MCP-wire 那层双反斜杠转义
// (\"ActiveState\" 形态), 单层文档转义一次解析即得终值。
// ★ 失败永不 throw: 非零退出 / stdout 不可解析 / spawn 失败 / 看门狗超时,
// 一律归一化为结构化 TxFailure(kind 判别), 由调用方(main.ts, todo 27)渲染。
// ★ 看门狗与 shell 通道完全同源: resolveWatchdogTimeoutMs + SIGKILL +
// rc 124 + "copilot exec timeout" 文案 + activeProcesses 清理链。
// ===========================================================================

/** 事务生命周期状态(与 internal/tx Status 线协议 token 逐字节一致)。 */
export type TxStatus =
  | "proposed"
  | "applying"
  | "applied"
  | "rolled_back"
  | "failed";

/** 步骤执行规范化结果 —— JSON 键是 internal/tx/tx.go OpResult 钉死的线上契约。 */
export interface TxOpResult {
  returncode: number;
  stdout?: string;
  stderr?: string;
  error?: string;
}

/** 事务步骤 —— 线上键保持 snake_case(args/before_state/after_state/op_result), 不重映射。 */
export interface TxStep {
  index: number;
  adapter: string;
  args?: unknown;
  before_state?: unknown;
  after_state?: unknown;
  op_result: TxOpResult;
}

/** 事务日志全文 —— internal/tx.Transaction 的序列化形态(Go 侧五键)。 */
export interface TxJournal {
  id: string;
  created_at: string;
  status: TxStatus;
  steps?: TxStep[];
  rollback_plan: { steps?: TxStep[] };
}

/** 结构化失败判别: 未找到 / 用法(exit 2) / 运行期(exit 1) / 看门狗 / spawn 失败 / 解析失败。 */
export type TxErrorKind =
  | "tx_not_found"
  | "tx_usage_error"
  | "tx_runtime_error"
  | "tx_timeout"
  | "tx_spawn_error"
  | "tx_parse_error";

/** execTx* 的统一失败形态(永不 throw 的唯一出口)。 */
export interface TxFailure {
  ok: false;
  kind: TxErrorKind;
  returncode: number;
  error: string;
  stderr: string;
}

/** propose 成功: 日志全文 + 新末步的 Before/AfterState 便捷视图。 */
export interface TxProposeSuccess {
  ok: true;
  journal: TxJournal;
  step: TxStep;
  beforeState: unknown;
  afterState: unknown;
}

/** preview 的单个 diff 条目: 一步的 计划前 → 计划后 视图。 */
export interface TxPreviewDiffEntry {
  index: number;
  adapter: string;
  args: unknown;
  beforeState: unknown;
  afterState: unknown;
}

/** preview 成功: 日志全文 + 渲染用的步骤 diff 视图(status 子命令的 typed 形态)。 */
export interface TxPreviewSuccess {
  ok: true;
  journal: TxJournal;
  diff: TxPreviewDiffEntry[];
}

/** apply 成功: 日志全文 + 每步 op_result 集合(线上契约的 OpResult set)。 */
export interface TxApplySuccess {
  ok: true;
  journal: TxJournal;
  opResults: TxOpResult[];
}

export type TxProposeOutcome = TxProposeSuccess | TxFailure;
export type TxPreviewOutcome = TxPreviewSuccess | TxFailure;
export type TxApplyOutcome = TxApplySuccess | TxFailure;

// 状态全集校验表(线协议 fail-closed: 未知 status token 视为解析失败)。
const TX_STATUS_SET = new Set<string>([
  "proposed",
  "applying",
  "applied",
  "rolled_back",
  "failed",
]);

// "transaction <16hex> not found" 是 cmdApply/cmdStatus 的未找到哨兵
// (commands.go mustLoad), 归一化为 tx_not_found 供调用方判别。
const TX_NOT_FOUND_RE = /^transaction [a-f0-9]{16} not found$/;

/** runTxCli 的原始结果: 在 ExecResult 形状之外带一个失败通道判别(kind)。 */
interface TxRawRun {
  stdout: string;
  stderr: string;
  returncode: number;
  kind: "" | "timeout" | "spawn_error";
  error: string | null;
}

/**
 * 派生一次 resolveTxBinary() 指向的 daedalus-tx(argv 直传, 无 shell 解释),
 * 收集 stdout/stderr 直至进程退出, 全程套用与 execAllowlisted 同源的
 * 看门狗(超时 SIGKILL, rc 124)。本函数不做任何 JSON 解析。
 */
async function runTxCli(argv: string[]): Promise<TxRawRun> {
  ensureSignalHandlerRegistered();

  const txBinary = resolveTxBinary();

  const CommandConstructor = (globalThis as any).Deno?.Command;
  if (!CommandConstructor) {
    throw new Error(
      "Deno.Command is not available in the current runtime environment",
    );
  }

  // daedalus-tx 是一次性 CLI: stdin 无需管道, stdout/stderr 收集到完成。
  let childProcess: any;
  try {
    childProcess = new CommandConstructor(txBinary, {
      args: argv,
      stdin: "null",
      stdout: "piped",
      stderr: "piped",
    }).spawn();
  } catch (err: unknown) {
    const msg = err instanceof Error ? err.message : String(err);
    return {
      stdout: "",
      stderr: `Failed to spawn daedalus-tx: ${msg}`,
      returncode: 1,
      kind: "spawn_error",
      error: msg,
    };
  }

  activeProcesses.add(childProcess);

  const timeoutMs = resolveWatchdogTimeoutMs();
  let timeoutId: any;
  const outputPromise = childProcess.output();

  try {
    const raced = await Promise.race([
      outputPromise.then((out: any) => ({ timedOut: false, out })),
      new Promise<{ timedOut: boolean; out?: any }>((resolve) => {
        timeoutId = setTimeout(() => {
          try {
            childProcess.kill("SIGKILL");
          } catch {
            // 如果已经终止则忽略
          }
          resolve({ timedOut: true });
        }, timeoutMs);
      }),
    ]);

    if (raced.timedOut) {
      return {
        stdout: "",
        stderr: "Timeout: command execution exceeded 40s",
        returncode: 124,
        kind: "timeout",
        error: "copilot exec timeout",
      };
    }

    const out = raced.out;
    const decoder = new TextDecoder();
    return {
      stdout: decoder.decode(out.stdout ?? new Uint8Array()),
      stderr: decoder.decode(out.stderr ?? new Uint8Array()),
      // 信号终止等异常退出时 code 为 null: 保守归一为非零(1), 绝不虚报成功。
      returncode: typeof out.code === "number" ? out.code : 1,
      kind: "",
      error: null,
    };
  } finally {
    if (timeoutId) {
      clearTimeout(timeoutId);
    }
    activeProcesses.delete(childProcess);
    // 看门狗命中后进程已被 SIGKILL, output() 随 stdio 关闭而兑现;
    // 这里等待回收, 确保不留悬空的子进程/异步 op(Deno 测试资源卫生)。
    try {
      await outputPromise;
    } catch {
      // 回收失败无需上报: 结果已定
    }
  }
}

/** 从 stdout 提取 todo 15 契约的唯一 JSON 文档(单行紧凑 + 换行)。 */
function parseTxStdout(
  stdout: string,
): { doc: unknown } | { parseError: string } {
  const lines = stdout.split("\n").map((l) => l.trim()).filter((l) =>
    l.length > 0
  );
  if (lines.length === 0) {
    return { parseError: "daedalus-tx stdout 为空(契约要求恰一份 JSON 文档)" };
  }
  try {
    return { doc: JSON.parse(lines[0]) };
  } catch {
    return {
      parseError: `daedalus-tx stdout 解析失败: ${lines[0].slice(0, 200)}`,
    };
  }
}

function makeTxFailure(
  kind: TxErrorKind,
  raw: TxRawRun,
  error: string,
): TxFailure {
  return {
    ok: false,
    kind,
    returncode: raw.returncode,
    error,
    stderr: raw.stderr,
  };
}

/**
 * 把一次原始 CLI 运行归一化为 成功文档 或 结构化失败:
 * timeout/spawn_error 直通; 非零退出优先取文档 {"error":...} 文本并按
 * 哨兵正则/退出码分派 kind(未找到/用法/运行期); 零退出但 stdout 不是
 * JSON 文档 → tx_parse_error。
 */
function interpretTxRun(
  raw: TxRawRun,
): { failure: TxFailure } | { doc: unknown } {
  if (raw.kind === "timeout") {
    return {
      failure: makeTxFailure("tx_timeout", raw, "copilot exec timeout"),
    };
  }
  if (raw.kind === "spawn_error") {
    return {
      failure: makeTxFailure(
        "tx_spawn_error",
        raw,
        raw.error ?? raw.stderr ?? "spawn failed",
      ),
    };
  }

  const parsed = parseTxStdout(raw.stdout);
  const doc = "parseError" in parsed
    ? null
    : (parsed.doc as Record<string, unknown> | null);

  if (raw.returncode !== 0) {
    // 非零退出优先按退出码分类(CLI 失败契约: 出错也吐 {"error":...});
    // stdout 无 error 文档时退而取 stderr 文本 —— 进程失败是事实, 解析失败只是症状。
    const errText = typeof doc?.error === "string" && doc.error.length > 0
      ? doc.error
      : ("parseError" in parsed
        ? (raw.stderr.trim() || parsed.parseError)
        : (raw.stderr.trim() ||
          `daedalus-tx exited with code ${raw.returncode}`));
    let kind: TxErrorKind = "tx_runtime_error";
    if (TX_NOT_FOUND_RE.test(errText)) {
      kind = "tx_not_found";
    } else if (raw.returncode === 2) {
      kind = "tx_usage_error";
    }
    return { failure: makeTxFailure(kind, raw, errText) };
  }

  if ("parseError" in parsed) {
    return { failure: makeTxFailure("tx_parse_error", raw, parsed.parseError) };
  }

  // 契约: error 文档必伴随非零退出。零退出却带 error 键属异常形态,
  // 保守归为运行期错误, 绝不让调用方误当成功。
  if (typeof doc?.error === "string" && doc.error.length > 0) {
    return { failure: makeTxFailure("tx_runtime_error", raw, doc.error) };
  }
  return { doc: parsed.doc };
}

/** 结构校验(fail-closed): 只认 internal/tx.Transaction 的五键形态。 */
function asJournal(doc: unknown): TxJournal | null {
  if (typeof doc !== "object" || doc === null) {
    return null;
  }
  const o = doc as Record<string, unknown>;
  if (typeof o.id !== "string") return null;
  if (typeof o.status !== "string" || !TX_STATUS_SET.has(o.status)) return null;
  if (o.steps !== undefined && !Array.isArray(o.steps)) return null;
  if (typeof o.rollback_plan !== "object" || o.rollback_plan === null) {
    return null;
  }
  return doc as TxJournal;
}

/**
 * `daedalus-tx propose <tx-id> <adapter> <args-json>` —— 追加一个适配器步骤
 * (仅快照, 无副作用)。返回新末步(step)与其 BeforeState/AfterState 视图;
 * 失败返回结构化 TxFailure(不 throw)。
 */
export async function execTxPropose(
  txId: string,
  adapter: string,
  args: unknown,
): Promise<TxProposeOutcome> {
  const raw = await runTxCli([
    "propose",
    txId,
    adapter,
    JSON.stringify(args ?? {}),
  ]);
  const run = interpretTxRun(raw);
  if ("failure" in run) return run.failure;

  const journal = asJournal(run.doc);
  if (!journal) {
    return makeTxFailure(
      "tx_parse_error",
      raw,
      "propose 输出不是可识别的事务日志文档(id/status/rollback_plan)",
    );
  }
  const steps = journal.steps ?? [];
  const step = steps[steps.length - 1];
  if (!step) {
    return makeTxFailure(
      "tx_parse_error",
      raw,
      "propose 输出缺少新追加步骤(steps 为空)",
    );
  }
  return {
    ok: true,
    journal,
    step,
    beforeState: step.before_state,
    afterState: step.after_state,
  };
}

/**
 * `daedalus-tx status <tx-id>` —— 读取日志全文, 渲染为「计划前 → 计划后」的
 * 步骤 diff 视图(展示用, 无任何副作用); 失败返回结构化 TxFailure(不 throw)。
 */
export async function execTxPreview(txId: string): Promise<TxPreviewOutcome> {
  const raw = await runTxCli(["status", txId]);
  const run = interpretTxRun(raw);
  if ("failure" in run) return run.failure;

  const journal = asJournal(run.doc);
  if (!journal) {
    return makeTxFailure(
      "tx_parse_error",
      raw,
      "status 输出不是可识别的事务日志文档(id/status/rollback_plan)",
    );
  }
  const diff: TxPreviewDiffEntry[] = (journal.steps ?? []).map((s) => ({
    index: s.index,
    adapter: s.adapter,
    args: s.args,
    beforeState: s.before_state,
    afterState: s.after_state,
  }));
  return { ok: true, journal, diff };
}

/**
 * `daedalus-tx apply <tx-id>` —— 依序执行全部步骤(真实副作用), 返回每步
 * op_result 集合; 任一步失败时 CLI 以 {"error":...} + exit 1 结束, 本层
 * 归一化为结构化 TxFailure(tx_runtime_error 等), 绝不 throw。
 */
export async function execTxApply(txId: string): Promise<TxApplyOutcome> {
  const raw = await runTxCli(["apply", txId]);
  const run = interpretTxRun(raw);
  if ("failure" in run) return run.failure;

  const journal = asJournal(run.doc);
  if (!journal) {
    return makeTxFailure(
      "tx_parse_error",
      raw,
      "apply 输出不是可识别的事务日志文档(id/status/rollback_plan)",
    );
  }
  const opResults: TxOpResult[] = (journal.steps ?? []).map((s) => s.op_result);
  return { ok: true, journal, opResults };
}
