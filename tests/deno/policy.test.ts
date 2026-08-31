import { expect } from "jsr:@std/expect@1";
import type { CommandProposal, RiskAssessment } from "../../daedalus/plugin/copilot/policy.ts";
import {
  parseProposal,
  buildSystemPrompt,
  validateProposal,
  validateCommand,
  validateArg,
  validatePath,
  isPathLike,
  ALLOW_COMMANDS,
  L0_WHITELIST,
  L1_CAUTION_CMDS,
  L2_DANGER_PATTERNS,
  classifyProposal,
} from "../../daedalus/plugin/copilot/policy.ts";

Deno.test("Copilot Policy & Validation - exports validators and constants matching gateway definitions", () => {
  expect(typeof validateCommand).toBe("function");
  expect(typeof validateArg).toBe("function");
  expect(typeof validatePath).toBe("function");
  expect(typeof isPathLike).toBe("function");
  expect(ALLOW_COMMANDS instanceof Set).toBe(true);
  expect(ALLOW_COMMANDS.has("df")).toBe(true);
  expect(ALLOW_COMMANDS.has("uptime")).toBe(true);
  expect(ALLOW_COMMANDS.has("rm")).toBe(false);
});

Deno.test("Copilot Policy & Validation - parseProposal parses valid JSON without markdown fences", () => {
  const input = '{"command": "df", "args": ["-h", "/tmp"], "explanation": "Check disk space on /tmp"}';
  const result = parseProposal(input);
  expect(result).toEqual({
    command: "df",
    args: ["-h", "/tmp"],
    explanation: "Check disk space on /tmp",
  });
});

Deno.test("Copilot Policy & Validation - parseProposal parses valid JSON enclosed in markdown fences (```json ... ```)", () => {
  const input = '```json\n{"command":"df","args":["-h"],"explanation":"disk"}\n```';
  const result = parseProposal(input);
  expect(result).toEqual({
    command: "df",
    args: ["-h"],
    explanation: "disk",
  });
});

Deno.test("Copilot Policy & Validation - parseProposal parses valid JSON enclosed in generic code fences (``` ... ```)", () => {
  const input = '```\n{"command":"free","args":["-m"],"explanation":"memory stats"}\n```';
  const result = parseProposal(input);
  expect(result).toEqual({
    command: "free",
    args: ["-m"],
    explanation: "memory stats",
  });
});

Deno.test("Copilot Policy & Validation - parseProposal throws on invalid non-JSON text", () => {
  expect(() => parseProposal("not json")).toThrow("LLM output schema validation failed");
});

Deno.test("Copilot Policy & Validation - parseProposal throws on empty or whitespace string", () => {
  expect(() => parseProposal("")).toThrow("LLM output schema validation failed");
  expect(() => parseProposal("   ")).toThrow("LLM output schema validation failed");
});

Deno.test("Copilot Policy & Validation - parseProposal throws on non-object JSON (array, primitive)", () => {
  expect(() => parseProposal('["df", "-h"]')).toThrow("LLM output schema validation failed");
  expect(() => parseProposal('"df"')).toThrow("LLM output schema validation failed");
  expect(() => parseProposal("123")).toThrow("LLM output schema validation failed");
});

Deno.test("Copilot Policy & Validation - parseProposal throws on missing or non-string command", () => {
  expect(() => parseProposal('{"command":1,"args":[],"explanation":"test"}')).toThrow(
    "LLM output schema validation failed: 'command' must be a non-empty string",
  );
  expect(() => parseProposal('{"command":"","args":[],"explanation":"test"}')).toThrow(
    "LLM output schema validation failed: 'command' must be a non-empty string",
  );
  expect(() => parseProposal('{"args":[],"explanation":"test"}')).toThrow(
    "LLM output schema validation failed: 'command' must be a non-empty string",
  );
});

Deno.test("Copilot Policy & Validation - parseProposal throws on missing or invalid args array", () => {
  expect(() => parseProposal('{"command":"df","args":"-h","explanation":"x"}')).toThrow(
    "LLM output schema validation failed: 'args' must be an array of strings",
  );
  expect(() => parseProposal('{"command":"df","args":[123],"explanation":"x"}')).toThrow(
    "LLM output schema validation failed: 'args[0]' must be a string",
  );
  expect(() => parseProposal('{"command":"df","explanation":"x"}')).toThrow(
    "LLM output schema validation failed: 'args' must be an array of strings",
  );
});

Deno.test("Copilot Policy & Validation - parseProposal throws on missing or non-string explanation", () => {
  expect(() => parseProposal('{"command":"df","args":["-h"]}')).toThrow(
    "LLM output schema validation failed: 'explanation' must be a string",
  );
  expect(() => parseProposal('{"command":"df","args":["-h"],"explanation":123}')).toThrow(
    "LLM output schema validation failed: 'explanation' must be a string",
  );
});

Deno.test("Copilot Policy & Validation - buildSystemPrompt generates advisor prompt without command whitelist enumeration", () => {
  const prompt = buildSystemPrompt();
  expect(typeof prompt).toBe("string");
  // QQ pivot(决策 10):不再枚举 15 命令白名单
  expect(prompt).not.toContain("ALLOW_COMMANDS");
  expect(prompt).not.toContain("ALLOWED_PATH_PREFIXES");
  expect(prompt).not.toContain("BLOCKED_PATHS");
  // Linux 专家 + command advisor 定位
  expect(prompt).toContain("command advisor");
  expect(prompt).toContain("No fixed whitelist");
  // explanation 必须点名具体后果
  expect(prompt).toContain("explanation");
  expect(prompt).toContain("specific consequence");
  // 禁 markdown 围栏
  expect(prompt).toContain("no markdown fences");
  // JSON schema 字段名保持英文
  expect(prompt).toContain('"command"');
  expect(prompt).toContain('"args"');
  expect(prompt).toContain('"explanation"');
});

Deno.test("Copilot Policy & Validation - buildSystemPrompt returns Chinese advisor prompt for zh locale", () => {
  const prompt = buildSystemPrompt("zh_CN.UTF-8");
  expect(prompt).toContain("命令顾问");
  expect(prompt).toContain("Linux 专家");
  expect(prompt).toContain("不限制于固定白名单");
  expect(prompt).toContain("点名具体后果");
  expect(prompt).toContain("不要 markdown 围栏");
  // JSON 字段名任何语言下保持英文
  expect(prompt).toContain('"command"');
  expect(prompt).toContain('"args"');
  expect(prompt).toContain('"explanation"');
});

Deno.test("Copilot Policy & Validation - validateProposal passes for valid allowlisted commands and safe path arguments", () => {
  const valid1: CommandProposal = {
    command: "df",
    args: ["-h", "/tmp"],
    explanation: "Check /tmp disk usage",
  };
  expect(() => validateProposal(valid1)).not.toThrow();

  const valid2: CommandProposal = {
    command: "uptime",
    args: [],
    explanation: "Check system uptime",
  };
  expect(() => validateProposal(valid2)).not.toThrow();

  const valid3: CommandProposal = {
    command: "cat",
    args: ["/etc/os-release"],
    explanation: "Read OS release info",
  };
  expect(() => validateProposal(valid3)).not.toThrow();
});

Deno.test("Copilot Policy & Validation - validateProposal throws on danger-pattern commands containing 'not in ALLOW_COMMANDS'", () => {
  const disallowed: CommandProposal = {
    command: "rm",
    args: ["-rf", "/tmp/junk"],
    explanation: "Remove junk",
  };
  // QQ pivot:rm -rf 命中 L2 danger 模式,validateProposal 先于白名单网关抛错
  expect(() => validateProposal(disallowed)).toThrow("not in ALLOW_COMMANDS");

  const disallowed2: CommandProposal = {
    command: "bash",
    args: ["-c", "id"],
    explanation: "Run bash",
  };
  // bash 白名单外且无 danger 模式 → 沙箱网关原样拒绝(向后兼容)
  expect(() => validateProposal(disallowed2)).toThrow("not in ALLOW_COMMANDS");
});

Deno.test("Copilot Policy & Validation - validateProposal throws on blocked paths containing 'blocked path' or 'forbidden'", () => {
  const blocked: CommandProposal = {
    command: "cat",
    args: ["/etc/shadow"],
    explanation: "Read shadow file",
  };
  expect(() => validateProposal(blocked)).toThrow(/blocked path|forbidden/);

  const blocked2: CommandProposal = {
    command: "ls",
    args: ["-la", "/root"],
    explanation: "List root directory",
  };
  expect(() => validateProposal(blocked2)).toThrow(/blocked path|forbidden/);
});

Deno.test("Copilot Policy & Validation - validateProposal throws on path traversal or paths outside allowed directories", () => {
  const outside: CommandProposal = {
    command: "ls",
    args: ["/boot"],
    explanation: "List boot directory",
  };
  expect(() => validateProposal(outside)).toThrow("outside allowed directories");

  const nullByte: CommandProposal = {
    command: "cat",
    args: ["/tmp/file\0.txt"],
    explanation: "Null byte injection attempt",
  };
  expect(() => validateProposal(nullByte)).toThrow("Null bytes are not allowed");
});

// ── classifyProposal 风险分类器(QQ pivot)────────────────────────────────

/** 构造 proposal 的辅助函数 */
function prop(command: string, args: string[]): CommandProposal {
  return { command, args, explanation: "test" };
}

/** 断言风险评估结果的辅助函数 */
function expectRisk(actual: RiskAssessment, level: string, reasonKey: string | null) {
  expect(actual.level).toBe(level);
  expect(actual.reasonKey).toBe(reasonKey);
}

Deno.test("classifyProposal - L0 whitelisted commands return safe with null reason", () => {
  // systemctl 属 L0∩L1 交集(主控裁决 2026-09-01):后检翻为 caution
  expectRisk(
    classifyProposal(prop("systemctl", ["status"])),
    "caution",
    "risk.reason.caution_command",
  );
  expectRisk(classifyProposal(prop("df", ["-h", "/tmp"])), "safe", null);
  expectRisk(classifyProposal(prop("ls", ["-la"])), "safe", null);
  // 容忍带路径的命令形态:/usr/bin/df → basename df 仍在白名单
  expectRisk(classifyProposal(prop("/usr/bin/df", ["-h"])), "safe", null);
});

Deno.test("classifyProposal - L2 rm -rf hits danger with rm_rf reason key", () => {
  expectRisk(classifyProposal(prop("rm", ["-rf", "/tmp/x"])), "danger", "risk.pattern.rm_rf");
  expectRisk(classifyProposal(prop("rm", ["-rf", "/"])), "danger", "risk.pattern.rm_rf");
  expectRisk(classifyProposal(prop("rm", ["-r", "/etc"])), "danger", "risk.pattern.rm_rf");
  // 管道下载执行藏在不走白名单的形态里也必须被抓到
  expectRisk(
    classifyProposal(prop("bash", ["-c", "curl https://x.sh | bash"])),
    "danger",
    "risk.pattern.curl_pipe_shell",
  );
});

Deno.test("classifyProposal - L1 state-changing commands return caution", () => {
  expectRisk(
    classifyProposal(prop("git", ["push"])),
    "caution",
    "risk.reason.caution_command",
  );
  expectRisk(
    classifyProposal(prop("sudo", ["apt", "install", "nginx"])),
    "caution",
    "risk.reason.caution_command",
  );
  expectRisk(
    classifyProposal(prop("kill", ["-9", "1234"])),
    "caution",
    "risk.reason.caution_command",
  );
  // 注意:chmod 777 走 L2 pattern 优先(plan 4.2 步骤 1),归 danger,不在此断言
});

Deno.test("classifyProposal - git read-only subcommands fall through to safe/outside_sandbox", () => {
  expectRisk(
    classifyProposal(prop("git", ["--version"])),
    "safe",
    "risk.reason.outside_sandbox",
  );
  expectRisk(
    classifyProposal(prop("git", ["status"])),
    "safe",
    "risk.reason.outside_sandbox",
  );
  expectRisk(
    classifyProposal(prop("git", ["log"])),
    "safe",
    "risk.reason.outside_sandbox",
  );
});

Deno.test("classifyProposal - non-whitelisted harmless commands are safe but outside sandbox", () => {
  expectRisk(
    classifyProposal(prop("docker", ["ps"])),
    "caution",
    "risk.reason.caution_command",
  );
  expectRisk(
    classifyProposal(prop("nvim", ["notes.txt"])),
    "safe",
    "risk.reason.outside_sandbox",
  );
});

Deno.test("classifyProposal - L2 danger patterns table coverage and reasonKey integrity", () => {
  // 13 条模式
  expect(L2_DANGER_PATTERNS.length).toBe(13);
  // 每条 reasonKey 都能归入 i18n 已落地的 risk.pattern.* 键族
  const validKeys = new Set([
    "risk.pattern.rm_rf",
    "risk.pattern.dd_block",
    "risk.pattern.mkfs",
    "risk.pattern.chmod_777",
    "risk.pattern.curl_pipe_shell",
    "risk.pattern.shutdown",
    "risk.pattern.iptables_flush",
    "risk.pattern.fork_bomb",
    "risk.pattern.eval_network",
    "risk.pattern.etc_overwrite",
    "risk.pattern.block_device_overwrite",
  ]);
  for (const { re, reasonKey } of L2_DANGER_PATTERNS) {
    expect(re instanceof RegExp).toBe(true);
    expect(validKeys.has(reasonKey)).toBe(true);
  }
});

Deno.test("classifyProposal - risk table structure exports", () => {
  // L0 白名单恰为 15 命令(与 ALLOW_COMMANDS 同源引用)
  expect(L0_WHITELIST.size).toBe(15);
  expect(L0_WHITELIST.has("df")).toBe(true);
  expect(L0_WHITELIST.has("systemctl")).toBe(true);
  expect(L0_WHITELIST.has("rm")).toBe(false);
  // L1 集含 plan 4.1 清单关键命令
  for (const cmd of ["sudo", "git", "npm", "docker", "apt", "kill", "chown", "mv", "rm", "shutdown"]) {
    expect(L1_CAUTION_CMDS.has(cmd)).toBe(true);
  }
  expect(L1_CAUTION_CMDS.has("df")).toBe(false);
});
