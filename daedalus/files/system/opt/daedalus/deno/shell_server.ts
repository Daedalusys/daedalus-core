/**
 * Daedalus OS Shell MCP 服务器 (Deno)。
 *
 * 为 "daedalus-shell-deno" 实现标准 JSON-RPC 2.0 stdio MCP 服务器。
 * 通过显式参数白名单、路径验证参数、净化执行环境、30 秒超时以及结构化仅追加审计日志，
 * 提供安全的命令执行。
 *
 * 设计为在 Deno 运行时权限边界下运行：
 *   deno run --allow-run=/usr/bin/df,/usr/bin/ls,... shell_server.ts
 */

// 显式允许的诊断/只读命令
export const DEFAULT_ALLOW_COMMANDS = new Set([
  "df",
  "ls",
  "cat",
  "pwd",
  "uname",
  "free",
  "ps",
  "uptime",
  "whoami",
  "ip",
  "arch",
  "hostname",
  "date",
  "ping",
  "systemctl",
]);

// 允许通过环境变量覆盖或扩展允许的命令
const envCommands = Deno.env.get("ALLOW_COMMANDS");
export const ALLOW_COMMANDS: Set<string> = envCommands
  ? new Set(envCommands.split(",").map((c) => c.trim()).filter((c) => c.length > 0))
  : DEFAULT_ALLOW_COMMANDS;

// 路径类参数允许的路径前缀
export const ALLOWED_PATH_PREFIXES = [
  "/home",
  "/var/log",
  "/tmp",
  "/proc",
  "/sys",
  "/etc/os-release",
  "/usr/lib/os-release",
  "/etc/fedora-release",
  "/etc/almalinux-release",
];

// 显式禁止的敏感路径
export const BLOCKED_PATHS = [
  "/etc/shadow",
  "/etc/gshadow",
  "/etc/sudoers",
  "/etc/sudoers.d",
  "/root",
];

// 干净的执行环境
const CLEAN_ENV: Record<string, string> = {
  PATH: "/usr/bin:/bin:/usr/sbin:/sbin",
  LANG: "C.UTF-8",
};

const TIMEOUT_MS = 30000;
const AUDIT_LOG_PATH = Deno.env.get("DAEDALUS_AUDIT_LOG_PATH") || "/var/log/daedalus/audit.jsonl";

/**
 * 判断参数是否类似于文件系统路径。
 */
export function isPathLike(arg: string): boolean {
  if (arg.includes("\0")) {
    return true;
  }
  if (
    arg.startsWith("/") ||
    arg.includes("/") ||
    arg === "." ||
    arg === ".." ||
    arg.startsWith("..")
  ) {
    return true;
  }
  return false;
}

/**
 * 规范化并验证路径参数。
 * 确保路径不触及受阻路径且保持在允许的目录范围内。
 */
export function validatePath(pathStr: string): string {
  if (typeof pathStr !== "string" || pathStr.length === 0) {
    throw new Error("Path must be a non-empty string.");
  }
  if (pathStr.includes("\0")) {
    throw new Error("Null bytes are not allowed in path arguments.");
  }

  // 若路径存在则解析规范 realpath，若不存在则进行规范化
  let resolved: string;
  try {
    resolved = Deno.realPathSync(pathStr);
  } catch {
    // 若路径在磁盘上尚不存在，则相对于 cwd 或根路径解析
    const absolute = pathStr.startsWith("/")
      ? pathStr
      : `${Deno.cwd()}/${pathStr}`;
    resolved = normalizePath(absolute);
  }

  // 检查显式禁止的路径
  for (const blocked of BLOCKED_PATHS) {
    const cleanBlocked = blocked.replace(/\/+$/, "");
    if (resolved === cleanBlocked || resolved.startsWith(cleanBlocked + "/")) {
      throw new Error(`Access to blocked path '${pathStr}' (${resolved}) is forbidden.`);
    }
  }

  // 检查允许的目录前缀
  let allowed = false;
  for (const prefix of ALLOWED_PATH_PREFIXES) {
    const cleanPrefix = prefix.replace(/\/+$/, "");
    if (resolved === cleanPrefix || resolved.startsWith(cleanPrefix + "/")) {
      allowed = true;
      break;
    }
  }

  if (!allowed) {
    throw new Error(
      `Path '${pathStr}' (resolved: ${resolved}) is outside allowed directories: ${ALLOWED_PATH_PREFIXES.join(", ")}`,
    );
  }

  return resolved;
}

/**
 * 针对不存在路径的基础路径规范化工具。
 */
function normalizePath(path: string): string {
  const parts = path.split("/").filter((p) => p.length > 0 && p !== ".");
  const stack: string[] = [];
  for (const part of parts) {
    if (part === "..") {
      stack.pop();
    } else {
      stack.push(part);
    }
  }
  return "/" + stack.join("/");
}

/**
 * 验证单个参数是否包含空字节和嵌入路径。
 */
export function validateArg(arg: string): void {
  if (typeof arg !== "string") {
    throw new Error(`Argument must be a string, got ${typeof arg}`);
  }
  if (arg.includes("\0")) {
    throw new Error("Null bytes are not allowed in arguments.");
  }

  if (arg.includes("=") && (arg.startsWith("-") || arg.startsWith("--"))) {
    const eqIdx = arg.indexOf("=");
    const val = arg.slice(eqIdx + 1);
    if (isPathLike(val)) {
      validatePath(val);
    }
  } else if (isPathLike(arg)) {
    validatePath(arg);
  }
}

/**
 * 依据白名单验证命令名称，并确保无路径遍历行为。
 */
export function validateCommand(command: string): string {
  if (!command || typeof command !== "string") {
    throw new Error("Command must be a non-empty string.");
  }
  if (command.includes("\0")) {
    throw new Error("Null bytes are not allowed in command.");
  }

  const trimmed = command.trim();
  const lastSlash = trimmed.lastIndexOf("/");
  const cmdBase = lastSlash >= 0 ? trimmed.slice(lastSlash + 1) : trimmed;

  if (!ALLOW_COMMANDS.has(cmdBase)) {
    throw new Error(`Command '${command}' is not in ALLOW_COMMANDS allowlist.`);
  }

  if (trimmed.includes("/")) {
    let cmdDir = trimmed.slice(0, lastSlash);
    try {
      cmdDir = Deno.realPathSync(cmdDir);
    } catch {
      // 若 realPathSync 失败则保持 cmdDir 原样
    }
    const allowedBinDirs = new Set(["/usr/bin", "/bin", "/usr/sbin", "/sbin"]);
    if (!allowedBinDirs.has(cmdDir)) {
      throw new Error(`Command path '${command}' is not in a valid system bin directory.`);
    }
  }

  return cmdBase;
}

/**
 * 追加审计日志条目。
 */
export async function recordAudit(
  command: string,
  args: string[],
  allowed: boolean,
  returncode: number | null,
  error?: string,
): Promise<void> {
  const entry = {
    timestamp: Date.now() / 1000,
    tool: "shell_exec",
    command,
    args,
    allowed,
    returncode,
    error: error || null,
  };

  try {
    const encoder = new TextEncoder();
    const line = encoder.encode(JSON.stringify(entry) + "\n");
    // 如果可能则追加写入审计日志
    await Deno.writeFile(AUDIT_LOG_PATH, line, { append: true, create: false });
  } catch {
    // 忽略审计日志写入失败（例如测试期间为只读系统或目录缺失）
  }
}

export interface ShellExecResult {
  stdout: string;
  stderr: string;
  returncode: number;
  error?: string;
}

/**
 * 使用 Deno.Command 安全执行命令。
 */
export async function shellExec(
  command: string,
  args: string[] = [],
): Promise<ShellExecResult> {
  const effectiveArgs = args || [];

  // 1. 验证命令
  let cmdToRun: string;
  try {
    cmdToRun = validateCommand(command);
  } catch (err: unknown) {
    const msg = err instanceof Error ? err.message : String(err);
    await recordAudit(command, effectiveArgs, false, 126, msg);
    return {
      stdout: "",
      stderr: `Command validation failed: ${msg}`,
      returncode: 126,
      error: msg,
    };
  }

  // 2. 验证所有参数
  try {
    for (const arg of effectiveArgs) {
      validateArg(arg);
    }
  } catch (err: unknown) {
    const msg = err instanceof Error ? err.message : String(err);
    await recordAudit(command, effectiveArgs, false, 126, msg);
    return {
      stdout: "",
      stderr: `Argument validation failed: ${msg}`,
      returncode: 126,
      error: msg,
    };
  }

  // 3. 使用 Deno.Command 及超时机制执行进程
  try {
    const denoCmd = new Deno.Command(cmdToRun, {
      args: effectiveArgs,
      stdout: "piped",
      stderr: "piped",
      env: CLEAN_ENV,
    });

    const proc = denoCmd.spawn();

    let timeoutId: number | undefined;
    const timeoutPromise = new Promise<never>((_, reject) => {
      timeoutId = (setTimeout(() => {
        try {
          proc.kill("SIGKILL");
        } catch {
          // 进程可能已经结束
        }
        reject(new Error(`Command timed out after ${TIMEOUT_MS / 1000} seconds`));
      }, TIMEOUT_MS) as unknown) as number;
    });

    try {
      const output = await Promise.race([proc.output(), timeoutPromise]);
      if (timeoutId !== undefined) {
        clearTimeout(timeoutId);
      }

      const decoder = new TextDecoder();
      const stdout = decoder.decode(output.stdout);
      const stderr = decoder.decode(output.stderr);
      const returncode = output.code;

      await recordAudit(command, effectiveArgs, true, returncode);
      return {
        stdout,
        stderr,
        returncode,
      };
    } catch (raceErr: unknown) {
      if (timeoutId !== undefined) {
        clearTimeout(timeoutId);
      }
      const msg = raceErr instanceof Error ? raceErr.message : String(raceErr);
      await recordAudit(command, effectiveArgs, true, 124, msg);
      return {
        stdout: "",
        stderr: msg,
        returncode: 124,
        error: msg,
      };
    }
  } catch (execErr: unknown) {
    const msg = execErr instanceof Error ? execErr.message : String(execErr);
    await recordAudit(command, effectiveArgs, true, 1, msg);
    return {
      stdout: "",
      stderr: `Execution failed: ${msg}`,
      returncode: 1,
      error: msg,
    };
  }
}

/**
 * 标准 MCP 工具定义与元数据
 */
const TOOLS = [
  {
    name: "shell_exec",
    description:
      "Execute an allowlisted command with arguments safely (read-only / diagnostic). Restricted to an argv allowlist with strict argument path validation and 30s timeout.",
    inputSchema: {
      type: "object",
      properties: {
        command: {
          type: "string",
          description: "The command name to execute (must be in ALLOW_COMMANDS).",
        },
        args: {
          type: "array",
          items: { type: "string" },
          description: "List of command-line arguments to pass.",
        },
      },
      required: ["command"],
    },
    annotations: {
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: false,
    },
  },
];

/**
 * JSON-RPC 2.0 stdio MCP 协议处理器
 */
export async function handleMessage(message: Record<string, unknown>): Promise<Record<string, unknown> | null> {
  const jsonrpc = message.jsonrpc;
  const id = message.id;
  const method = message.method;
  const params = (message.params as Record<string, unknown>) || {};

  if (jsonrpc !== "2.0") {
    if (id !== undefined) {
      return {
        jsonrpc: "2.0",
        id,
        error: { code: -32600, message: "Invalid Request: jsonrpc must be '2.0'" },
      };
    }
    return null;
  }

  // 处理通知 / 请求
  switch (method) {
    case "initialize": {
      return {
        jsonrpc: "2.0",
        id,
        result: {
          protocolVersion: "2024-11-05",
          capabilities: {
            tools: {
              listChanged: false,
            },
          },
          serverInfo: {
            name: "daedalus-shell-deno",
            version: "1.0.0",
          },
        },
      };
    }

    case "notifications/initialized":
    case "initialized": {
      return null;
    }

    case "ping": {
      return {
        jsonrpc: "2.0",
        id,
        result: {},
      };
    }

    case "tools/list": {
      return {
        jsonrpc: "2.0",
        id,
        result: {
          tools: TOOLS,
        },
      };
    }

    case "tools/call": {
      const toolName = params.name as string;
      const toolArguments = (params.arguments as Record<string, unknown>) || {};

      if (toolName === "shell_exec") {
        const cmd = String(toolArguments.command || "");
        const rawArgs = toolArguments.args;
        const argsList: string[] = Array.isArray(rawArgs)
          ? rawArgs.map((a) => String(a))
          : [];

        const execResult = await shellExec(cmd, argsList);
        const isError = execResult.returncode !== 0;

        return {
          jsonrpc: "2.0",
          id,
          result: {
            content: [
              {
                type: "text",
                text: JSON.stringify(execResult, null, 2),
              },
            ],
            isError,
          },
        };
      }

      return {
        jsonrpc: "2.0",
        id,
        error: {
          code: -32601,
          message: `Method '${toolName}' not found`,
        },
      };
    }

    default: {
      if (id !== undefined) {
        return {
          jsonrpc: "2.0",
          id,
          error: {
            code: -32601,
            message: `Method '${method}' not found`,
          },
        };
      }
      return null;
    }
  }
}

/**
 * 按行读取 JSON-RPC 消息的主标准输入输出循环。
 */
export async function runServer(): Promise<void> {
  const decoder = new TextDecoder();
  const encoder = new TextEncoder();

  const buf = new Uint8Array(65536);
  let leftover = "";

  while (true) {
    const bytesRead = await Deno.stdin.read(buf);
    if (bytesRead === null) {
      break; // 文件结束 (EOF)
    }

    leftover += decoder.decode(buf.subarray(0, bytesRead));
    const lines = leftover.split("\n");
    leftover = lines.pop() || "";

    for (const line of lines) {
      const trimmed = line.trim();
      if (!trimmed) {
        continue;
      }

      try {
        const parsed = JSON.parse(trimmed);
        const response = await handleMessage(parsed);
        if (response) {
          await Deno.stdout.write(encoder.encode(JSON.stringify(response) + "\n"));
        }
      } catch (err: unknown) {
        const errorResponse = {
          jsonrpc: "2.0",
          id: null,
          error: {
            code: -32700,
            message: `Parse error: ${err instanceof Error ? err.message : String(err)}`,
          },
        };
        await Deno.stdout.write(encoder.encode(JSON.stringify(errorResponse) + "\n"));
      }
    }
  }
}

// 直接作为入口点执行时运行服务器
if (import.meta.main) {
  runServer();
}
