/**
 * Daedalus OS Copilot 策略与验证。
 *
 * 实现严格的提议验证、LLM 输出解析以及确定性的系统提示词生成。
 *
 * ★ 冻结副本声明 ★
 * 下方的白名单常量与网关校验器（validateCommand / validateArg /
 * validatePath / isPathLike）是 Go 侧 `daedalus/core/internal/shellpolicy`
 * 的**保持一致的冻结副本**（原始来源为已退役的 Deno shell 服务器实现）。
 * Copilot 在进程内做与 `daedalus-shell` Go 二进制完全相同的策略校验，
 * 保证“提议即执行”路径零策略偏离。
 * **修改 Go 侧 internal/shellpolicy 时必须同步修改本文件。**
 */

// 显式允许的诊断/只读命令（与 internal/shellpolicy 默认白名单一致的冻结副本，15 项）
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

// 路径类参数允许的路径前缀（与 internal/shellpolicy 一致的冻结副本，9 项）
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

// 显式禁止的敏感路径（与 internal/shellpolicy 一致的冻结副本，5 项）
export const BLOCKED_PATHS = [
  "/etc/shadow",
  "/etc/gshadow",
  "/etc/sudoers",
  "/etc/sudoers.d",
  "/root",
];

/**
 * 判断参数是否类似于文件系统路径。
 * （internal/shellpolicy 冻结副本）
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
 * 针对不存在路径的基础路径规范化工具。
 * （internal/shellpolicy 冻结副本）
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
 * 规范化并验证路径参数。
 * 确保路径不触及受阻路径且保持在允许的目录范围内。
 * （internal/shellpolicy 冻结副本）
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
 * 验证单个参数是否包含空字节和嵌入路径。
 * （internal/shellpolicy 冻结副本）
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
 * （internal/shellpolicy 冻结副本）
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

export interface CommandProposal {
  command: string;
  args: string[];
  explanation: string;
}

/**
 * 将 LLM 输出解析并严格验证为 CommandProposal。
 * 如果存在 markdown 代码块标记则予以去除，并强制检查数据结构格式。
 */
export function parseProposal(text: string): CommandProposal {
  if (typeof text !== "string" || text.trim().length === 0) {
    throw new Error("LLM output schema validation failed: output must be non-empty text");
  }

  // 如果存在 markdown 代码块标记则予以去除（例如 ```json ... ``` 或 ``` ... ```）
  const cleanedText = text.replace(/```json|```/g, "").trim();

  let parsed: unknown;
  try {
    parsed = JSON.parse(cleanedText);
  } catch (err: unknown) {
    const msg = err instanceof Error ? err.message : String(err);
    throw new Error(`LLM output schema validation failed: invalid JSON: ${msg}`);
  }

  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
    throw new Error("LLM output schema validation failed: root must be a JSON object");
  }

  const obj = parsed as Record<string, unknown>;

  if (typeof obj.command !== "string" || obj.command.trim().length === 0) {
    throw new Error("LLM output schema validation failed: 'command' must be a non-empty string");
  }

  if (!Array.isArray(obj.args)) {
    throw new Error("LLM output schema validation failed: 'args' must be an array of strings");
  }

  for (let i = 0; i < obj.args.length; i++) {
    if (typeof obj.args[i] !== "string") {
      throw new Error(`LLM output schema validation failed: 'args[${i}]' must be a string`);
    }
  }

  if (typeof obj.explanation !== "string") {
    throw new Error("LLM output schema validation failed: 'explanation' must be a string");
  }

  return {
    command: obj.command.trim(),
    args: obj.args as string[],
    explanation: obj.explanation,
  };
}

/**
 * 构建用于 LLM 命令转换的不可变、确定性系统提示词。
 */
export function buildSystemPrompt(): string {
  const allowedCommandsList = Array.from(ALLOW_COMMANDS).sort().join(", ");
  const allowedPrefixesList = ALLOWED_PATH_PREFIXES.join(", ");
  const blockedPathsList = BLOCKED_PATHS.join(", ");

  return `You are Daedalus OS Copilot, an AI assistant built into the operating system.
Your sole job is to translate user natural language requests into a single safe diagnostic / inspection command proposal.

RULES AND CONSTRAINTS:
1. You MUST only propose commands from this strict allowlist (ALLOW_COMMANDS):
   Allowed commands: [${allowedCommandsList}]

2. PATH SAFETY RULES:
   - Path arguments must be absolute and within allowed prefixes (ALLOWED_PATH_PREFIXES): [${allowedPrefixesList}]
   - Explicitly forbidden sensitive paths (BLOCKED_PATHS): [${blockedPathsList}]
   - Path traversal (..) is strictly forbidden.
   - Relative paths without leading slash are forbidden.
   - Null bytes (\\0) are strictly forbidden.

3. OUTPUT FORMAT:
   You must output ONLY valid JSON matching this exact schema:
   {
     "command": "<command name from allowlist>",
     "args": ["<arg1>", "<arg2>", ...],
     "explanation": "<brief human-readable explanation of what this command does>"
   }

4. STRICT PROHIBITIONS:
   - Output ONLY the JSON object. Do NOT include any markdown explanations, commentary, or text outside the JSON.
   - NEVER suggest or use commands outside the allowlist (e.g., rm, bash, sh, sudo, dd, chmod, chown are strictly forbidden).
   - NEVER use shell pipelines (|), redirects (>), subshells ($()), or chaining (&&, ;, ||). Command arguments must be a structured list of strings.
   - If the user's intent cannot be achieved using the allowed commands, choose the safest allowed diagnostic command or explain the limitation in the explanation field while keeping the command allowed (e.g. command: "uptime", explanation: "Requested action not permitted by OS safety policy.").`;
}

/**
 * 依据 DaedalusShell 安全网关规则验证 CommandProposal。
 * 若任一验证器拒绝则抛出错误。
 */
export function validateProposal(proposal: CommandProposal): void {
  if (!proposal || typeof proposal !== "object") {
    throw new Error("Invalid proposal: proposal must be an object");
  }

  // 1. 根据 ALLOW_COMMANDS 和二进制路径验证命令
  validateCommand(proposal.command);

  // 2. 验证每个参数
  if (Array.isArray(proposal.args)) {
    for (const arg of proposal.args) {
      validateArg(arg);
    }
  }
}
