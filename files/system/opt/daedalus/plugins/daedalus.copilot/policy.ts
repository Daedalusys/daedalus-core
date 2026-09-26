/**
 * ★ 冻结副本声明 ★ 下方白名单常量与网关校验器（validateCommand/validateArg/validatePath/isPathLike）是
 * Go 侧 `daedalus-core/internal/shellpolicy` 的冻结副本，Copilot 进程内校验与 `daedalus-shell` 二进制零策略偏离。
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

// 风险分级模型（产品定位从「白名单拒绝」转向「本地 classify 标注」）：
//   L0 = 沙箱可执行集（沿用 15 命令白名单，与 policy.toml 三点防漂移链一致）；
//   L1 = caution（改系统状态但通常可回滚）；
//   L2 = danger（通常不可逆，架构上无法被 daedalus-shell 执行）。
// 风险判定权在本地静态表：classifyProposal 不读 LLM 输出的任何
// 风险自标注字段，防 prompt injection 操纵。
// 风险表内联于本文件：是产品设计决策，非用户可配置的运行时策略，
// 绝不放 policy.toml，避免扩大白名单三点防漂移链。

export const L0_WHITELIST: ReadonlySet<string> = DEFAULT_ALLOW_COMMANDS;

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

/** （internal/shellpolicy 冻结副本） */
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

/** 针对不存在路径的基础路径规范化工具。（internal/shellpolicy 冻结副本） */
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

/** 规范化并验证路径参数:不触及受阻路径且保持在允许的目录范围内。（internal/shellpolicy 冻结副本） */
export function validatePath(pathStr: string): string {
  if (typeof pathStr !== "string" || pathStr.length === 0) {
    throw new Error("Path must be a non-empty string.");
  }
  if (pathStr.includes("\0")) {
    throw new Error("Null bytes are not allowed in path arguments.");
  }

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

  for (const blocked of BLOCKED_PATHS) {
    const cleanBlocked = blocked.replace(/\/+$/, "");
    if (resolved === cleanBlocked || resolved.startsWith(cleanBlocked + "/")) {
      throw new Error(`Access to blocked path '${pathStr}' (${resolved}) is forbidden.`);
    }
  }

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

/** 验证单个参数无空字节和嵌入路径。（internal/shellpolicy 冻结副本） */
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

/** 白名单验证命令名称 + 路径遍历检查。（internal/shellpolicy 冻结副本） */
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

/** 将 LLM 输出严格验证为 CommandProposal（结构格式强校验）。 */
export function parseProposal(text: string): CommandProposal {
  if (typeof text !== "string" || text.trim().length === 0) {
    throw new Error("LLM output schema validation failed: output must be non-empty text");
  }

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
 * ★ 按 locale 选语言：zh_CN/zh* 变体 → 中文 prompt（explanation 自然用中文、
 * 与 query 语言一致），locale 为空或非 zh → 英文原文。
 * ★ JSON schema 字段名（command/args/explanation）任何语言下都保持英文，
 * 译文只翻引导性说明、不翻字段名——否则 LLM 输出的 JSON 解析会失败。
 * 设计要点：不枚举白名单，LLM 以 Linux 专家身份生成命令；风险判定在本地静态表。
 */
export function buildSystemPrompt(locale?: string): string {
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

/** RiskAssessment.tx_kind 的取值集合（事务分类维度）。 */
export type RiskTxKind = "shell" | "tx_propose" | "tx_apply" | "tx_rollback";

/**
 * level：L0=safe / L1=caution / L2=danger；
 * reasonKey：i18n key（risk.pattern.* / risk.reason.*），UI 层经 t() 渲染；safe/null 表示白名单内且无危险模式命中。
 * tx_kind：事务分类扩展字段，**可选**——classifyProposal 的全部
 * 既有 shell 路径不携带该键，缺省（undefined）即视为 "shell"（消费方按
 * `risk.tx_kind ?? "shell"` 归一），保证既有输出逐对象不变、向后兼容；
 * classifyTxProposal 则恒显式设置三类 tx 意图之一。
 */
export type RiskAssessment = {
  level: "safe" | "caution" | "danger";
  reasonKey: string | null;
  tx_kind?: RiskTxKind;
};

/** git 只读子命令豁免：不改系统状态，虽在 L1 集仍落「白名单外 safe」（clone/log/diff 等非 caution）。 */
const GIT_READONLY_SUBCOMMANDS = new Set([
  "clone", "log", "diff", "status", "show", "branch", "remote",
  "describe", "rev-parse", "blame", "ls-files", "version", "--version",
  "help", "config --list", "ls-remote", "cat-file", "shortlog", "reflog",
]);

/**
 * 本地风险分类器（纯静态表查，不读 LLM 风险自标注）；四步算法见函数内编号注释。
 * ★ 竞态条款 ★ 事务意图绝不进入本 shell 路径：tx_propose /
 * tx_apply / tx_rollback 由 classifyTxProposal 独立定级（不查 L0_WHITELIST），
 * 消费方据此跳过 shell_exec 分派，L0 路径与 tx 路径互不抢占（no race）。
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

  const lastSlash = cmd.lastIndexOf("/");
  const cmdBase = lastSlash >= 0 ? cmd.slice(lastSlash + 1) : cmd;

  // 2. L0 白名单（含 L0∩L1 交集后检：systemctl 等整命令族统一 ⚠️，子命令细分留后续）
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

  // 4. 白名单外 → safe（不可执行；风险来源是 daedalus-shell 拒 → 用户手动复制）
  return { level: "safe", reasonKey: "risk.reason.outside_sandbox" };
}

// v1 事务动词集与 daedalus-tx service.set 适配器保持一致：
// started|stopped|restarted|enabled|disabled；分类器与适配器若漂移，
// 会出现"分类放行 / 执行拒绝"或反向的分裂语义，改一侧必查另一侧。
const TX_APPLY_SAFE_STATES: ReadonlySet<string> = new Set(["started", "stopped"]);

// v1 事务状态词与 daedalus-tx package.set 适配器
// 保持一致：present|absent|latest；present/absent 是确定性操作（与 service 的
// started/stopped 同档 L0），latest 依赖 dnf 仓库元数据、可能拉网络 → L1。
// 分类器与适配器若漂移，同样出现"分类放行 / 执行拒绝"分裂语义，改一侧必查另一侧。
const TX_APPLY_PACKAGE_L0_STATES: ReadonlySet<string> = new Set(["present", "absent"]);

/**
 * ★ L0_WHITELIST 竞态条款 ★ 本分类器把 L0_WHITELIST 推理扩展到事务：
 * 意图是事务时跳过 shell_exec 分派（事务性意图绝不走 shell 路径），使 L0
 * 执行路径与 tx 执行路径互不竞争——tx_* 提议永不被当作白名单 shell 命令
 * 重复定级。
 * 规则表（纯静态查表，不读 LLM 风险自标注）：tx_propose → safe/null；tx_apply
 * 按（域 × 期望态）查 TX_APPLY_*（safe/caution；reload → danger，适配器已拒绝
 * reload）；tx_rollback → caution；未知动词/状态词 → caution（fail-closed）。
 * intent 不在 tx_propose/tx_apply/tx_rollback 集合 → 抛错（静态表无此行的
 * neither-safe 兜底，配置期 bug 必须响亮暴露，fail-closed）。
 * target 为空 → 抛错（断言字面量 "classifyTxProposal: target is
 * empty"，文案勿改）。
 * ★ package 域形态 ★ 与 service 域完全同构（args[1] 期望态位置不变），仅
 * target 首词为 `package <name>`；name 的合法性（单段、无 `@` 组语法、字符集
 * 白名单）由 daedalus-tx 侧 parsePackageSetArgs/sanitizePackageName 把关，
 * 分类器不校验 name 形态，只按 (域 × 期望态) 查表定级。
 * reasonKey 仅复用已落地 i18n 键：reload→danger 借用语义最近的
 * "risk.pattern.shutdown"（中断在途服务）；tx.* 专用键族由后续落地
 * 后替换，本文件届时同步——在此之前不得引用不存在的键。
 */
export function classifyTxProposal(
  intent: string,
  target: string,
  desiredState: string,
): RiskAssessment {
  if (typeof target !== "string" || target.trim().length === 0) {
    throw new Error("classifyTxProposal: target is empty");
  }

  const intentNorm = typeof intent === "string" ? intent.trim() : "";
  switch (intentNorm) {
    case "tx_propose":
      return { level: "safe", reasonKey: null, tx_kind: "tx_propose" };

    case "tx_apply": {
      const state = typeof desiredState === "string" ? desiredState.trim() : "";
      if (/^package\s+\S+$/.test(target.trim())) {
        if (TX_APPLY_PACKAGE_L0_STATES.has(state)) {
          return { level: "safe", reasonKey: null, tx_kind: "tx_apply" };
        }
        return { level: "caution", reasonKey: "risk.reason.caution_command", tx_kind: "tx_apply" };
      }
      if (TX_APPLY_SAFE_STATES.has(state)) {
        return { level: "safe", reasonKey: null, tx_kind: "tx_apply" };
      }
      if (state === "reload") {
        return { level: "danger", reasonKey: "risk.pattern.shutdown", tx_kind: "tx_apply" };
      }
      return { level: "caution", reasonKey: "risk.reason.caution_command", tx_kind: "tx_apply" };
    }

    case "tx_rollback":
      // 状态回退：意图驱动、可预期，但仍改系统状态 → caution
      return { level: "caution", reasonKey: "risk.reason.caution_command", tx_kind: "tx_rollback" };

    default:
      throw new Error(`classifyTxProposal: unknown intent '${intent}'`);
  }
}

/**
 * 安全网关验证（向后兼容门）：仅 L2 danger 抛错（copilot_reject/exit 126），
 * L1/白名单外只标注不拒。错误消息保留 "not in ALLOW_COMMANDS"——main.test.ts 断言依赖，勿改。
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

  const risk = classifyProposal(proposal);
  if (risk.level === "danger") {
    throw new Error(
      `Command '${proposal.command}' matches a danger pattern (${risk.reasonKey}) and is not in ALLOW_COMMANDS.`,
    );
  }

  // 白名单外由沙箱网关拒绝；此检查同时覆盖 L1/L2 的二进制目录校验
  validateCommand(proposal.command);

  for (const arg of proposal.args) {
    validateArg(arg);
  }
}
