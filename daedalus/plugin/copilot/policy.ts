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

// ─────────────────────────────────────────────────────────────────────────────
// 风险分级模型（QQ pivot，决策 2/3/8）
//
// 产品定位从「白名单拒绝」转向「本地 classify 标注」：
//   L0 = 沙箱可执行集（沿用 15 命令白名单，与 policy.toml 三点防漂移链一致）；
//   L1 = caution（改系统状态但通常可回滚）；
//   L2 = danger（通常不可逆，架构上无法被 daedalus-shell 执行）。
// 风险判定权在本地静态表（决策 2）：classifyProposal 不读 LLM 输出的任何
// 风险自标注字段，防 prompt injection 操纵。
// 风险表内联于本文件（决策 8）：是产品设计决策，非用户可配置的运行时策略，
// 绝不放 policy.toml，避免扩大白名单三点防漂移链。
// ─────────────────────────────────────────────────────────────────────────────

// L0 沙箱可执行集：即现有 15 命令白名单（DEFAULT_ALLOW_COMMANDS）的别名，
// 不新造集合，保证与 policy.toml / internal/shellpolicy 防漂移链零偏离（决策 4）。
export const L0_WHITELIST: ReadonlySet<string> = DEFAULT_ALLOW_COMMANDS;

// L1 caution 命令集（改系统状态，通常可回滚；plan 4.1 节清单原样）
export const L1_CAUTION_CMDS: ReadonlySet<string> = new Set([
  "sudo", "doas",
  "git",     // push/commit/reset/clean 触发 caution；clone/log/diff 等只读子命令走豁免逻辑
  "npm",     // install/publish 等
  "yarn", "pnpm", "bun",
  "pip", "pip3", "poetry", "uv",  // install/uninstall
  "docker", "podman",             // rm/rmi/exec/stop
  "kubectl",
  "apt", "apt-get", "dnf", "yum", "pacman", "zypper",  // install/remove
  "systemctl",  // 已在 L0；此 set 仅供 classifyProposal 加 caution 标签
  "service",
  "kill", "pkill", "killall",
  "mount", "umount",
  "chown", "chmod",   // 任何形式
  "mv", "rm",         // 递归+强删或危险路径时升级 L2；默认 L1
  "cp",
  "tar", "zip", "unzip",
  "shutdown", "reboot", "halt", "poweroff",  // 实际由 L2 shutdown 模式先行命中
]);

// L2 danger 模式（任何命中 → danger；reasonKey 与 i18n/*.json 已落地的
// risk.pattern.* key 严格对应，勿新造名字）
export const L2_DANGER_PATTERNS: ReadonlyArray<{ re: RegExp; reasonKey: string }> = [
  // rm 携带递归+强制双旗标（-rf/-fr/-Rf、拆分 -r -f、长旗标组合）：
  // 即使目标是普通路径也可能批量不可逆删除
  {
    re: /\brm\s+(?:-[a-zA-Z]*(?:[rR][a-zA-Z]*[fF]|[fF][a-zA-Z]*[rR])[a-zA-Z]*|--recursive\s+--force|--force\s+--recursive|-r\s+-f\b|-f\s+-r\b)(?:\s|$)/,
    reasonKey: "risk.pattern.rm_rf",
  },
  // rm 递归旗标 + 危险路径（根/家目录/系统目录）：直接掏空系统或用户数据
  {
    re: /\brm\s+(?:-[a-zA-Z]*[rR][a-zA-Z]*\s+)+(?:\/(?:etc|var|usr|boot|home|root|sys|proc)(?:\/|\s|$)|\/(?!\w)|~|\$\{?HOME\}?)/,
    reasonKey: "risk.pattern.rm_rf",
  },
  // dd 同时指定 if= 与 of=/dev/块设备：按字节直写磁盘，全盘数据被覆盖
  {
    re: /\bdd\s+.*\bif=.*\b(?:of|conv)=\/dev\/(?:sd|hd|nvme|vd|mmcblk|xvd)/,
    reasonKey: "risk.pattern.dd_block",
  },
  // dd 只要有 of=/dev/块设备：无论输入源是什么，目标盘原有数据即被清掉
  {
    re: /\bdd\s+.*\bof=\/dev\/(?:sd|hd|nvme|vd|mmcblk|xvd)/,
    reasonKey: "risk.pattern.dd_block",
  },
  // mkfs 家族：格式化文件系统，目标设备上所有数据瞬间清零
  {
    re: /\bmkfs(?:\.[a-z0-9]+)?\b/,
    reasonKey: "risk.pattern.mkfs",
  },
  // chmod 777（含 -R 递归）：权限全开，任何用户/进程可改写目标，提权与篡改温床
  {
    re: /\bchmod\s+(?:-[a-zA-Z]*[Rr][a-zA-Z]*\s+)?[0-7]*[67][67][67]\b/,
    reasonKey: "risk.pattern.chmod_777",
  },
  // curl/wget/fetch 输出直接管道给 shell：从网络下载并立即执行，典型供应链攻击面
  {
    re: /\b(?:curl|wget|fetch)\b[^|;&]*\|\s*(?:sudo\s+)?(?:ba|z|da|fi)?sh\b/,
    reasonKey: "risk.pattern.curl_pipe_shell",
  },
  // shutdown/reboot/halt/poweroff：关机或重启，中断所有会话与在途写入
  {
    re: /\b(?:shutdown|reboot|halt|poweroff)\b/,
    reasonKey: "risk.pattern.shutdown",
  },
  // iptables 清空规则（-F/--flush，含合并旗标如 -nF）：防火墙清零，攻击面直接暴露
  {
    re: /\biptables\s+(?:\S+\s+)*(?:-[A-Za-z]*F\b|--flush\b)/,
    reasonKey: "risk.pattern.iptables_flush",
  },
  // fork 炸弹 :(){ :|:& };: 形态：自我复制的进程风暴，瞬间耗尽 PID/内存
  {
    re: /:\(\)\s*\{\s*:\s*\|\s*:\s*&\s*\}\s*;\s*:\s*/,
    reasonKey: "risk.pattern.fork_bomb",
  },
  // eval 执行网络下载内容（$(curl ...) / 反引号形态）：不可验证来源的任意代码执行
  {
    re: /\beval\s+(?:\$\(\s*(?:curl|wget|fetch)\b|`(?:curl|wget|fetch))/,
    reasonKey: "risk.pattern.eval_network",
  },
  // 重定向覆盖 /etc、/boot 下文件：系统关键配置/引导文件被覆写，可能变砖
  {
    re: />\s*\/(?:etc|boot)\//,
    reasonKey: "risk.pattern.etc_overwrite",
  },
  // 重定向覆盖 /dev/块设备：绕过文件系统直写裸设备，数据不可恢复
  {
    re: />\s*\/dev\/(?:sd|hd|nvme|vd|mmcblk|xvd)/,
    reasonKey: "risk.pattern.block_device_overwrite",
  },
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
 * 构建用于 LLM 命令转换的不可变、确定性系统提示词（QQ command advisor 定位）。
 *
 * ★ 本函数按 locale 选 prompt 语言 ★
 * - locale 为 zh_CN / zh（或其它 zh_* 变体）时返回中文 prompt，
 *   LLM 在中文 prompt 下 explanation 字段会自然用中文输出，与 query 语言一致；
 * - locale 为空或非 zh 范围时返回英文原文。
 * 注意：JSON schema 字段名（command/args/explanation）在任何语言下都保持英文，
 * 译文只翻引导性说明，不翻字段名——否则 LLM 输出的 JSON 解析会失败。
 *
 * 设计要点（决策 10）：不再枚举 15 命令白名单，LLM 作为 Linux 专家生成最合适的
 * 命令；风险判定交给本地 classifyProposal 静态表，LLM 不参与风险自标注（决策 2）。
 */
export function buildSystemPrompt(locale?: string): string {
  // locale 归一化：zh_CN / zh-xx / zh_* 均视为中文；空值与其它语言一律英文
  const normalized = (locale ?? "").trim().replace(/-/g, "_").toLowerCase();
  const useChinese = normalized === "zh" || normalized.startsWith("zh_");

  if (useChinese) {
    return `你是 Daedalus 命令顾问（daedalus-copilot），内嵌于操作系统的 Linux 专家。
你的工作：把用户自然语言请求翻译成 shell 命令。

只输出 JSON：
{
  "command": "<可执行名>",
  "args": ["<参数1>", "<参数2>", ...],
  "explanation": "<一句话：此命令做什么——若会改系统状态，必须说明具体后果>"
}

规则：
- 生成最合适的命令，不限制于固定白名单。
- 若命令可能改变系统状态（安装/重启/删除 等），\`explanation\` 必须**点名具体后果**（如"会停掉 nginx 服务并丢失正在处理的连接"）。模糊描述如"可能改系统状态"不够。
- 不要发明 flag。不确定时输出更小、更通用的子集。
- 只回 JSON，不要散文，不要 markdown 围栏。`;
  }

  return `You are Daedalus command advisor, a Linux expert integrated into the operating system.
Your job: translate user natural language requests into shell commands.

Output JSON only:
{
  "command": "<executable name>",
  "args": ["<arg1>", "<arg2>", ...],
  "explanation": "<one sentence: what this command does, and — for state-changing commands — what specific consequence the user should know before running it>"
}

Rules:
- Generate the most appropriate command for the user's request. No fixed whitelist.
- If a command may modify system state (install, restart, delete, etc.), the \`explanation\` MUST name the specific consequence (e.g., "this will stop the nginx service and drop in-flight connections"). Vague phrases like "may change system state" are not enough.
- Never invent flags. If you are unsure, output a smaller, well-known subset.
- Reply with JSON only, no prose, no markdown fences.`;
}

/**
 * 风险评估结果（QQ pivot）。
 * level：L0=safe / L1=caution / L2=danger；
 * reasonKey：i18n key（risk.pattern.* / risk.reason.*），UI 层经 t() 渲染；
 * safe/null 表示白名单内且无危险模式命中。
 */
export type RiskAssessment = {
  level: "safe" | "caution" | "danger";
  reasonKey: string | null;
};

/**
 * git 只读子命令豁免：这些子命令不改系统状态，虽 git 在 L1 集，
 * 仍落到「白名单外 safe」标签（plan 4.1 节注释：clone/log/diff 仍是
 * 非 caution 的普通命令）。
 */
const GIT_READONLY_SUBCOMMANDS = new Set([
  "clone", "log", "diff", "status", "show", "branch", "remote",
  "describe", "rev-parse", "blame", "ls-files", "version", "--version",
  "help", "config --list", "ls-remote", "cat-file", "shortlog", "reflog",
]);

/**
 * 本地风险分类器（决策 2：纯静态表查，不读 LLM 输出的任何风险自标注字段）。
 * 四步算法（plan 4.2）：
 *   1. L2 模式优先——command+args 全文匹配，任一命中即 danger；
 *   2. L0 白名单（仅取 basename，容忍 /usr/bin/df 形态）→ safe/null；
 *   3. L1 集（git 只读子命令豁免后）→ caution/"risk.reason.caution_command"；
 *   4. 白名单外、非 caution/danger → safe/"risk.reason.outside_sandbox"
 *      （不可执行，但无害；用户手动执行）。
 */
export function classifyProposal(proposal: CommandProposal): RiskAssessment {
  if (!proposal || typeof proposal !== "object") {
    throw new Error("Invalid proposal: proposal must be an object");
  }

  const cmd = proposal.command;
  const args = Array.isArray(proposal.args) ? proposal.args : [];
  // L2 全文匹配：command + args 拼接，覆盖管道/重定向藏在 args 里的形态
  const fullCmd = [cmd, ...args].join(" ");

  // 1. L2 模式优先（最危险）
  for (const { re, reasonKey } of L2_DANGER_PATTERNS) {
    if (re.test(fullCmd)) {
      return { level: "danger", reasonKey };
    }
  }

  // 命令 basename 归一化（/usr/bin/df → df）
  const lastSlash = cmd.lastIndexOf("/");
  const cmdBase = lastSlash >= 0 ? cmd.slice(lastSlash + 1) : cmd;

  // 2. L0 白名单(含 L0∩L1 交集后检(主控裁决 2026-09-01):systemctl 等既在
  //    白名单又在 caution 集,整命令族统一 ⚠️,子命令细分留后续)
  if (L0_WHITELIST.has(cmdBase)) {
    if (L1_CAUTION_CMDS.has(cmdBase)) {
      return { level: "caution", reasonKey: "risk.reason.caution_command" };
    }
    return { level: "safe", reasonKey: null };
  }

  // 3. L1 caution 命令集；git 只读子命令豁免（clone/log/diff 等落到第 4 步）
  if (L1_CAUTION_CMDS.has(cmdBase)) {
    if (cmdBase === "git") {
      const sub = (args[0] ?? "").toLowerCase();
      if (!GIT_READONLY_SUBCOMMANDS.has(sub)) {
        return { level: "caution", reasonKey: "risk.reason.caution_command" };
      }
    } else {
      return { level: "caution", reasonKey: "risk.reason.caution_command" };
    }
  }

  // 4. 白名单外、非 caution/danger → safe（不可执行；风险来源是
  //    daedalus-shell 拒 → 用户得手动复制）
  return { level: "safe", reasonKey: "risk.reason.outside_sandbox" };
}

/**
 * 依据 DaedalusShell 安全网关规则验证 CommandProposal（向后兼容门）。
 *
 * QQ pivot 后内部改为调 classifyProposal：仅 L2 danger 时抛错（main.ts
 * 捕获后走 copilot_reject / exit 126 路径）；L1 caution / 白名单外 safe
 * 只标注不拒（L1/L2 架构上本就无法被 daedalus-shell 执行）。
 * 错误消息保留 "not in ALLOW_COMMANDS" 短语：main.test.ts 拒绝路径断言
 * 依赖该文案，向后兼容不破坏既有审计/退出码语义。
 */
export function validateProposal(proposal: CommandProposal): void {
  if (!proposal || typeof proposal !== "object") {
    throw new Error("Invalid proposal: proposal must be an object");
  }

  if (!Array.isArray(proposal.args)) {
    throw new Error("Invalid proposal: 'args' must be an array of strings");
  }

  if (typeof proposal.command !== "string" || proposal.command.trim().length === 0) {
    throw new Error("Invalid proposal: 'command' must be a non-empty string");
  }

  // 1. L2 danger 模式 → 抛错（含 basename，保留 ALLOW_COMMANDS 文案供兼容断言）
  const risk = classifyProposal(proposal);
  if (risk.level === "danger") {
    throw new Error(
      `Command '${proposal.command}' matches a danger pattern (${risk.reasonKey}) and is not in ALLOW_COMMANDS.`,
    );
  }

  // 2. 白名单外（safe/outside_sandbox、caution）→ 沙箱网关原样拒绝非白名单命令
  //    （此检查同时覆盖 L1/L2 的二进制目录校验；danger 已在步骤 1 先行拒绝）
  validateCommand(proposal.command);

  // 3. 参数校验（空字节 / 路径白名单，冻结副本语义不变）
  for (const arg of proposal.args) {
    validateArg(arg);
  }
}
