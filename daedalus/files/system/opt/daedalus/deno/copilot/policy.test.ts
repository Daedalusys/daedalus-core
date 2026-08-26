import { expect } from "jsr:@std/expect@1";
import type { CommandProposal } from "./policy.ts";
import {
  parseProposal,
  buildSystemPrompt,
  validateProposal,
  validateCommand,
  validateArg,
  validatePath,
  isPathLike,
  ALLOW_COMMANDS,
} from "./policy.ts";

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

Deno.test("Copilot Policy & Validation - buildSystemPrompt generates deterministic prompt containing all 15 allowlisted commands and path constraints", () => {
  const prompt = buildSystemPrompt();
  expect(typeof prompt).toBe("string");
  expect(prompt).toContain("ALLOW_COMMANDS");
  // 验证所有 15 个默认命令均被提及
  const commands = [
    "df", "ls", "cat", "pwd", "uname", "free", "ps",
    "uptime", "whoami", "ip", "arch", "hostname", "date",
    "ping", "systemctl",
  ];
  for (const cmd of commands) {
    expect(prompt).toContain(cmd);
  }
  // 验证路径前缀
  expect(prompt).toContain("/home");
  expect(prompt).toContain("/var/log");
  expect(prompt).toContain("/tmp");
  expect(prompt).toContain("/proc");
  expect(prompt).toContain("/sys");
  // 验证受阻/禁止路径
  expect(prompt).toContain("/etc/shadow");
  expect(prompt).toContain("/root");
  expect(prompt).toContain("/etc/sudoers");
  // 验证模式要求与限制
  expect(prompt).toContain('"command"');
  expect(prompt).toContain('"args"');
  expect(prompt).toContain('"explanation"');
  expect(prompt).toContain("ONLY valid JSON");
  expect(prompt).toContain("rm");
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

Deno.test("Copilot Policy & Validation - validateProposal throws on disallowed commands containing 'not in ALLOW_COMMANDS'", () => {
  const disallowed: CommandProposal = {
    command: "rm",
    args: ["-rf", "/tmp/junk"],
    explanation: "Remove junk",
  };
  expect(() => validateProposal(disallowed)).toThrow("not in ALLOW_COMMANDS");

  const disallowed2: CommandProposal = {
    command: "bash",
    args: ["-c", "id"],
    explanation: "Run bash",
  };
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
