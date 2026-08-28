/**
 * Daedalus OS Copilot 哈希链审计日志记录器。
 *
 * 使用 Deno.Command 调用 Go 审计 CLI（daedalus-audit），
 * 以确保 SHA-256 加密哈希链接和文件锁定（哈希链契约与 Python 版逐字节兼容）。
 * 当系统日志目录（/var/log/daedalus）不可写时，
 * 提供回退至非特权用户级目录（~/.local/share/daedalus）。
 *
 * 严禁使用 Deno 文件系统 API 直接写入审计文件。
 */

export const ALLOWED_AUDIT_TOOLS = new Set([
  "copilot_translate",
  "copilot_reject",
  "copilot_confirm",
  "copilot_edit",
  "copilot_cancel",
  "copilot_error",
]);

export type AuditTool =
  | "copilot_translate"
  | "copilot_reject"
  | "copilot_confirm"
  | "copilot_edit"
  | "copilot_cancel"
  | "copilot_error";

export type AuditOutcome = "success" | "denied" | "error";

/**
 * 探测文件/路径是否可访问（stat 成功即视为存在）。
 */
function pathExists(target: string): boolean {
  try {
    if (typeof (Deno as any).statSync === "function") {
      (Deno as any).statSync(target);
      return true;
    } else if (typeof (Deno as any).stat === "function") {
      (Deno as any).stat(target);
      return true;
    }
  } catch {
    // 不存在或不可访问
  }
  return false;
}

/**
 * 确定调用哈希链审计所使用的 Go CLI 二进制（daedalus-audit）路径。
 * 解析顺序：DAEDALUS_AUDIT_BIN 环境变量 → 生产默认 /usr/local/bin/daedalus-audit →
 *   开发态仓库构建产物 daedalus/core/bin/daedalus-audit（含向上回溯最多 10 层父目录）。
 * 全部探测失败时回退到生产路径（让用户看到友好错误）。
 */
export function getAuditBinary(): string {
  const envBinary = Deno.env.get("DAEDALUS_AUDIT_BIN");
  if (envBinary) {
    return envBinary;
  }
  const productionPath = "/usr/local/bin/daedalus-audit";
  if (pathExists(productionPath)) {
    return productionPath;
  }
  const devCandidates = [
    "daedalus/core/bin/daedalus-audit",
    "../daedalus/core/bin/daedalus-audit",
  ];
  for (const rel of devCandidates) {
    if (pathExists(rel)) {
      return rel;
    }
  }
  // 向上回溯最多 10 层父目录查找仓库内构建产物
  let cwd = Deno.cwd();
  for (let i = 0; i < 10; i++) {
    const tryPath = `${cwd}/daedalus/core/bin/daedalus-audit`;
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
 * 解析适用的审计日志文件路径。
 * 1. 若设置了 DAEDALUS_AUDIT_LOG_PATH 环境变量则优先使用。
 * 2. 尝试主系统路径 /var/log/daedalus/audit.jsonl。
 * 3. 若父目录不可访问/不可写，则回退至 $HOME/.local/share/daedalus/audit.jsonl。
 */
export function resolveAuditPath(): string {
  const envPath = Deno.env.get("DAEDALUS_AUDIT_LOG_PATH");
  if (envPath) {
    return envPath;
  }

  const primaryPath = "/var/log/daedalus/audit.jsonl";
  const parentDir = "/var/log/daedalus";
  const home = Deno.env.get("HOME") || "/root";
  const fallbackDir = `${home}/.local/share/daedalus`;
  const fallbackPath = `${fallbackDir}/audit.jsonl`;

  try {
    if (typeof (Deno as any).statSync === "function") {
      (Deno as any).statSync(parentDir);
    } else if (typeof (Deno as any).stat === "function") {
      (Deno as any).stat(parentDir);
    }
    return primaryPath;
  } catch (_err) {
    try {
      if (typeof (Deno as any).mkdirSync === "function") {
        (Deno as any).mkdirSync(fallbackDir, { recursive: true });
      } else if (typeof (Deno as any).mkdir === "function") {
        (Deno as any).mkdir(fallbackDir, { recursive: true });
      }
    } catch {
      // 忽略 mkdir 失败
    }
    return fallbackPath;
  }
}

/**
 * 通过调用 Go 审计 CLI（daedalus-audit）记录一条审计日志条目。
 * 捕获所有子进程故障和异常，记录至 console.error，
 * 并返回 false 而不抛出异常。
 *
 * @param tool 审计工具名称（必须在 ALLOWED_AUDIT_TOOLS 中）
 * @param args 参数载荷（对象或数组）
 * @param outcome 调用结果（'success' | 'denied' | 'error'）
 * @param logPath 可选的显式日志路径覆盖
 * @returns Promise<boolean> 若记录成功返回 true，否则返回 false
 */
export async function recordAudit(
  tool: string,
  args: Record<string, unknown> | unknown[],
  outcome: AuditOutcome,
  logPath?: string,
): Promise<boolean> {
  try {
    if (!ALLOWED_AUDIT_TOOLS.has(tool)) {
      console.error(`Invalid audit tool name '${tool}'. Must be one of: ${Array.from(ALLOWED_AUDIT_TOOLS).join(", ")}`);
      return false;
    }

    const binary = getAuditBinary();
    const resolvedLogPath = logPath ?? resolveAuditPath();
    const argsJson = JSON.stringify(args ?? {});

    const cmdArgs = [
      "--identity",
      "daedalus-copilot",
      "--tool",
      tool,
      "--args",
      argsJson,
      "--outcome",
      outcome,
      "--log-path",
      resolvedLogPath,
    ];

    const command = new Deno.Command(binary, {
      args: cmdArgs,
      stdout: "piped",
      stderr: "piped",
    });

    const output = await command.output();
    if (output.code === 0) {
      return true;
    }

    const stderrText = new TextDecoder().decode(output.stderr);
    console.error(`Audit logging process failed (code ${output.code}): ${stderrText}`);
    return false;
  } catch (err: unknown) {
    const msg = err instanceof Error ? err.message : String(err);
    console.error(`Audit logging invocation exception: ${msg}`);
    return false;
  }
}
