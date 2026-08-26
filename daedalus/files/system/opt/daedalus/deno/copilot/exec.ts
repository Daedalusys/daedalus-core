/**
 * Daedalus OS Copilot 命令执行桥接器。
 *
 * 启动沙箱化的 daedalus-shell MCP Deno 子进程，并通过标准输入输出执行按行分隔的
 * JSON-RPC 2.0 握手以调用 `shell_exec` 能力工具。
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
 * 解析用于派生 daedalus-shell MCP 服务器的 Deno 可执行文件路径。
 * 解析顺序：DAEDALUS_DENO_PATH 环境变量 → 当前进程的 Deno 可执行文件
 * （Deno.execPath()）→ 生产默认 /usr/local/bin/deno。
 * 开发态（asdf/nix/brew 等）下 execPath 指向真实安装路径，
 * 避免硬编码 /usr/local/bin/deno 导致子进程无法派生。
 */
function resolveDenoExecutable(): string {
  const envPath =
    typeof (globalThis as any).Deno?.env?.get === "function"
      ? (globalThis as any).Deno.env.get("DAEDALUS_DENO_PATH")
      : process.env.DAEDALUS_DENO_PATH;
  if (envPath) {
    return envPath;
  }
  if (typeof (globalThis as any).Deno?.execPath === "function") {
    try {
      return (globalThis as any).Deno.execPath();
    } catch {
      // execPath 不可用则回退
    }
  }
  return "/usr/local/bin/deno";
}

/**
 * 解析 daedalus-shell MCP 服务器脚本路径。
 * 解析顺序：DAEDALUS_SHELL_SERVER_PATH 环境变量 → 生产默认 /opt/daedalus/deno/shell_server.ts
 * → 开发态自动向上回溯至仓库内的 shell_server.ts。
 */
function resolveShellServerPath(): string {
  const envPath =
    typeof (globalThis as any).Deno?.env?.get === "function"
      ? (globalThis as any).Deno.env.get("DAEDALUS_SHELL_SERVER_PATH")
      : process.env.DAEDALUS_SHELL_SERVER_PATH;
  if (envPath) {
    return envPath;
  }

  const candidates = [
    "/opt/daedalus/deno/shell_server.ts",
    "daedalus/files/system/opt/daedalus/deno/shell_server.ts",
    "../daedalus/files/system/opt/daedalus/deno/shell_server.ts",
    "./shell_server.ts",
  ];
  for (const rel of candidates) {
    try {
      (globalThis as any).Deno.statSync(rel);
      return rel;
    } catch {
      // 文件不存在，尝试下一个候选
    }
  }
  // 向上回溯最多 10 层父目录查找仓库内路径
  let cwd = (globalThis as any).Deno.cwd();
  for (let i = 0; i < 10; i++) {
    const tryPath = `${cwd}/daedalus/files/system/opt/daedalus/deno/shell_server.ts`;
    try {
      (globalThis as any).Deno.statSync(tryPath);
      return tryPath;
    } catch {
      // 父级回溯
    }
    const parent = cwd.replace(/\/[^/]+\/?$/, "");
    if (parent === cwd) break;
    cwd = parent;
  }
  return "/opt/daedalus/deno/shell_server.ts";
}

/**
 * 通过派生的 daedalus-shell MCP 服务器执行已列入白名单的命令。
 */
export async function execAllowlisted(
  command: string,
  args: string[] = [],
): Promise<ExecResult> {
  ensureSignalHandlerRegistered();

  const denoExecutable = resolveDenoExecutable();
  const targetScript = resolveShellServerPath();

  const allowRun =
    "--allow-run=/usr/bin/df,/bin/df,/usr/bin/ls,/bin/ls,/usr/bin/cat,/bin/cat,/usr/bin/pwd,/bin/pwd,/usr/bin/uname,/bin/uname,/usr/bin/free,/bin/free,/usr/bin/ps,/bin/ps,/usr/bin/uptime,/bin/uptime,/usr/bin/whoami,/bin/whoami,/usr/bin/ip,/bin/ip,/usr/bin/arch,/bin/arch,/usr/bin/hostname,/bin/hostname,/usr/bin/date,/bin/date,/usr/bin/ping,/bin/ping,/usr/bin/systemctl,/bin/systemctl";
  const allowRead =
    "--allow-read=/home,/var/log,/tmp,/proc,/sys,/etc/os-release,/usr/lib/os-release,/etc/fedora-release,/etc/almalinux-release";
  const allowWrite = "--allow-write=/var/log/daedalus";
  // shell_server.ts 在模块顶层调用 Deno.env.get（ALLOW_COMMANDS、DAEDALUS_AUDIT_LOG_PATH），
  // 缺少 --allow-env 会导致子进程在加载阶段抛出 NotCapable 并立即退出（表现为 "Unexpected EOF"）。
  const allowEnv = "--allow-env";

  const spawnArgs = [
    "run",
    allowRun,
    allowRead,
    allowWrite,
    allowEnv,
    targetScript,
  ];

  const CommandConstructor = (globalThis as any).Deno?.Command;
  if (!CommandConstructor) {
    throw new Error("Deno.Command is not available in the current runtime environment");
  }

  const childCmd = new CommandConstructor(denoExecutable, {
    args: spawnArgs,
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

  const timeoutMs =
    Number(
      (typeof (globalThis as any).Deno?.env?.get === "function"
        ? (globalThis as any).Deno.env.get("DAEDALUS_WATCHDOG_TIMEOUT_MS")
        : process.env.DAEDALUS_WATCHDOG_TIMEOUT_MS) ?? 40000,
    );

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
