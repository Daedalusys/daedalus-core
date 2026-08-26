/**
 * Daedalus OS Copilot 策略与验证。
 *
 * 实现严格的提议验证、LLM 输出解析以及确定性的系统提示词生成。
 * 直接从 shell_server.ts 重新导出网关验证器，确保零策略偏离。
 */

export {
  validateCommand,
  validateArg,
  validatePath,
  isPathLike,
  ALLOW_COMMANDS,
  DEFAULT_ALLOW_COMMANDS,
  ALLOWED_PATH_PREFIXES,
  BLOCKED_PATHS,
} from "../shell_server.ts";

import {
  validateCommand,
  validateArg,
  ALLOW_COMMANDS,
  ALLOWED_PATH_PREFIXES,
  BLOCKED_PATHS,
} from "../shell_server.ts";

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
