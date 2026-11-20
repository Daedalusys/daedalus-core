/**
 * Daedalus OS Copilot 哈希链审计日志记录器：经 Deno.Command 调用 Go 审计
 * CLI（daedalus-audit）写入，保证 SHA-256 哈希链与文件锁契约（与 Python 版
 * 逐字节兼容）；/var/log/daedalus 不可写时回退 $HOME/.local/share/daedalus。
 * 严禁使用 Deno 文件系统 API 直接写入审计文件。
 */

export const ALLOWED_AUDIT_TOOLS = new Set([
  "copilot_translate",
  "copilot_reject",
  "copilot_confirm",
  "copilot_edit",
  "copilot_cancel",
  "copilot_error",
  // 事务通道专属审计事件族(由 main.ts runTxTurn 发出):
  // copilot_tx_propose = 事务开账并快照步骤提议;copilot_tx_apply = 用户授权后真实应用;
  // copilot_tx_reject = 用户在 y/n 处放弃(日志留 proposed 态,零副作用)。
  "copilot_tx_propose",
  "copilot_tx_apply",
  "copilot_tx_reject",
]);

export type AuditTool =
  | "copilot_translate"
  | "copilot_reject"
  | "copilot_confirm"
  | "copilot_edit"
  | "copilot_cancel"
  | "copilot_error"
  | "copilot_tx_propose"
  | "copilot_tx_apply"
  | "copilot_tx_reject";

export type AuditOutcome = "success" | "denied" | "error";

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
 * 审计 CLI（daedalus-audit）路径解析：DAEDALUS_AUDIT_BIN → 生产默认
 * /usr/local/bin/daedalus-audit → 仓库构建产物；全部探测失败回退生产路径。
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
    "daedalus-core/bin/daedalus-audit",
    "../daedalus-core/bin/daedalus-audit",
  ];
  for (const rel of devCandidates) {
    if (pathExists(rel)) {
      return rel;
    }
  }
  let cwd = Deno.cwd();
  for (let i = 0; i < 10; i++) {
    const tryPath = `${cwd}/daedalus-core/bin/daedalus-audit`;
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
 * 审计日志路径解析：DAEDALUS_AUDIT_LOG_PATH → /var/log/daedalus/audit.jsonl
 * → $HOME/.local/share/daedalus/audit.jsonl；两条都不可写返回空串。
 *
 * 判据是"当前用户能否真正写入"：实测创建并删除一个探测文件。仅看目录存在
 * 或 mode 位会被 root 与 ACL 骗过（非 root 对 /var/log/daedalus 可读可进不可写
 * 却 stat 成功），导致写入静默失败、哈希链审计丢失。
 */
export interface ResolveAuditPathOptions {
  primaryDir?: string;
  home?: string;
}

/** 实际落一个探测文件再删除；写不进去就是不可用（探测文件残留不影响判定）。 */
function directoryWritable(dir: string): boolean {
  const pid = (globalThis as any).Deno?.pid ?? 0;
  const probe = `${dir}/.daedalus-audit-write-probe-${pid}-${
    Math.random().toString(36).slice(2)
  }`;
  try {
    Deno.writeTextFileSync(probe, "");
  } catch {
    return false;
  } finally {
    try {
      Deno.removeSync(probe);
    } catch {
      // 写入失败时文件可能从未创建；清理失败不改变可写性判定
    }
  }
  return true;
}

/** mkdir -p；目录已存在视为成功，创建失败留给可写性探测判定。 */
function ensureDirectory(dir: string): boolean {
  try {
    Deno.mkdirSync(dir, { recursive: true });
    return true;
  } catch {
    return false;
  }
}

export function resolveAuditPath(opts: ResolveAuditPathOptions = {}): string {
  const envPath = Deno.env.get("DAEDALUS_AUDIT_LOG_PATH");
  if (envPath) {
    // 显式覆盖原样返回：不探测、不改写调用方的意图（探测失败也照样交给 CLI 报错）。
    return envPath;
  }

  const primaryDir = opts.primaryDir ?? "/var/log/daedalus";
  const home = (opts.home ?? Deno.env.get("HOME") ?? "/root").replace(/\/+$/, "");
  const fallbackDir = `${home}/.local/share/daedalus`;

  if (directoryWritable(primaryDir)) {
    return `${primaryDir}/audit.jsonl`;
  }
  if (ensureDirectory(fallbackDir) && directoryWritable(fallbackDir)) {
    return `${fallbackDir}/audit.jsonl`;
  }
  // 空串 = 明确"审计不可用"信号；绝不返回一条必然写入失败的路径。
  return "";
}

/**
 * 经 daedalus-audit CLI 记录一条审计条目；捕获全部子进程故障与异常、
 * 记至 console.error 并返回 false，绝不抛出（tool 必须在 ALLOWED_AUDIT_TOOLS 中）。
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
