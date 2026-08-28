import { expect } from "jsr:@std/expect@1";
import {
  resolveAuditPath,
  recordAudit,
  getAuditBinary,
  ALLOWED_AUDIT_TOOLS,
} from "../../daedalus/plugin/copilot/audit.ts";

let mockEnv: Record<string, string> = {};
let mockStatFailures = new Set<string>();
let mockCommandInvocations: { cmd: string; options: any }[] = [];
let mockCommandOutputFactory: () => Promise<{ code: number; stdout: Uint8Array; stderr: Uint8Array }> = async () => ({
  code: 0,
  stdout: new Uint8Array(),
  stderr: new Uint8Array(),
});

class AuditMockCommand {
  constructor(public cmd: string, public options: any) {
    mockCommandInvocations.push({ cmd, options });
  }
  async output() {
    return await mockCommandOutputFactory();
  }
  spawn() {
    const self = this;
    return {
      async output() {
        return await self.output();
      },
      kill() {},
    };
  }
}

let origDenoEnvGet: typeof Deno.env.get;
let origDenoStatSync: typeof Deno.statSync;
let origDenoStat: typeof Deno.stat;
let origDenoCommand: typeof Deno.Command;

function setup() {
  mockEnv = {};
  mockStatFailures.clear();
  mockCommandInvocations = [];
  mockCommandOutputFactory = async () => ({
    code: 0,
    stdout: new TextEncoder().encode('{"status":"ok"}'),
    stderr: new Uint8Array(),
  });

  origDenoEnvGet = Deno.env.get;
  origDenoStatSync = Deno.statSync;
  origDenoStat = Deno.stat;
  origDenoCommand = Deno.Command;

  (Deno.env as any).get = (key: string) => (mockEnv[key] !== undefined ? mockEnv[key] : origDenoEnvGet.call(Deno.env, key));
  (Deno as any).statSync = (p: string | URL) => {
    const pathStr = String(p);
    if (mockStatFailures.has(pathStr)) {
      throw new Error(`NotFound or PermissionDenied: ${pathStr}`);
    }
    return { isDirectory: true } as Deno.FileInfo;
  };
  (Deno as any).stat = async (p: string | URL) => {
    const pathStr = String(p);
    if (mockStatFailures.has(pathStr)) {
      throw new Error(`NotFound or PermissionDenied: ${pathStr}`);
    }
    return { isDirectory: true } as Deno.FileInfo;
  };
  (Deno as any).Command = AuditMockCommand;
}

function teardown() {
  if (origDenoEnvGet) (Deno.env as any).get = origDenoEnvGet;
  if (origDenoStatSync) (Deno as any).statSync = origDenoStatSync;
  if (origDenoStat) (Deno as any).stat = origDenoStat;
  if (origDenoCommand) (Deno as any).Command = origDenoCommand;
}

Deno.test("Copilot Audit - resolveAuditPath respects DAEDALUS_AUDIT_LOG_PATH environment variable override", () => {
  setup();
  try {
    mockEnv["DAEDALUS_AUDIT_LOG_PATH"] = "/custom/log/path/audit.jsonl";
    expect(resolveAuditPath()).toBe("/custom/log/path/audit.jsonl");
  } finally {
    teardown();
  }
});

Deno.test("Copilot Audit - resolveAuditPath returns primary path /var/log/daedalus/audit.jsonl when directory is accessible", () => {
  setup();
  try {
    expect(resolveAuditPath()).toBe("/var/log/daedalus/audit.jsonl");
  } finally {
    teardown();
  }
});

Deno.test("Copilot Audit - resolveAuditPath falls back to $HOME/.local/share/daedalus/audit.jsonl when /var/log/daedalus is inaccessible", () => {
  setup();
  try {
    mockStatFailures.add("/var/log/daedalus");
    mockEnv["HOME"] = "/home/testuser";
    expect(resolveAuditPath()).toBe("/home/testuser/.local/share/daedalus/audit.jsonl");
  } finally {
    teardown();
  }
});

Deno.test("Copilot Audit - getAuditBinary uses DAEDALUS_AUDIT_BIN when provided", () => {
  setup();
  try {
    mockEnv["DAEDALUS_AUDIT_BIN"] = "/custom/bin/daedalus-audit";
    expect(getAuditBinary()).toBe("/custom/bin/daedalus-audit");
  } finally {
    teardown();
  }
});

Deno.test("Copilot Audit - getAuditBinary resolves an existing daedalus-audit candidate without env override", () => {
  // 注意：本测试不使用 setup() 的 stat 桩件。
  // setup() 中 mock 的 statSync 对任意路径都返回成功，无法验证真实的多候选探测行为；
  // 因此这里直接操作真实环境变量与真实文件系统，并在结束时恢复，避免污染其他测试。
  const origEnv = Deno.env.get("DAEDALUS_AUDIT_BIN");
  try {
    Deno.env.delete("DAEDALUS_AUDIT_BIN");
    const resolved = getAuditBinary();
    let exists = false;
    try {
      Deno.statSync(resolved);
      exists = true;
    } catch {
      exists = false;
    }
    // 解析结果必须是磁盘上真实存在的 Go 审计 CLI（生产 /usr/local/bin 或开发态仓库构建产物）
    expect(exists).toBe(true);
    expect(resolved.endsWith("daedalus-audit")).toBe(true);
  } finally {
    // 恢复真实环境，确保环境变量被清理
    if (origEnv !== undefined) {
      Deno.env.set("DAEDALUS_AUDIT_BIN", origEnv);
    } else {
      Deno.env.delete("DAEDALUS_AUDIT_BIN");
    }
  }
});

Deno.test("Copilot Audit - recordAudit rejects non-allowlisted tool names and returns false without invoking subprocess", async () => {
  setup();
  try {
    const result = await recordAudit("unauthorized_tool" as any, {}, "success");
    expect(result).toBe(false);
    expect(mockCommandInvocations.length).toBe(0);
  } finally {
    teardown();
  }
});

Deno.test("Copilot Audit - recordAudit executes Go daedalus-audit CLI with correct flags, args JSON, and log path", async () => {
  setup();
  try {
    mockEnv["DAEDALUS_AUDIT_BIN"] = "/usr/local/bin/daedalus-audit";
    mockEnv["DAEDALUS_AUDIT_LOG_PATH"] = "/tmp/test-audit.jsonl";

    const toolArgs = { query: "check disk", proposal: { command: "df", args: ["-h"] } };
    const success = await recordAudit("copilot_translate", toolArgs, "success");

    expect(success).toBe(true);
    expect(mockCommandInvocations.length).toBe(1);

    const invocation = mockCommandInvocations[0];
    expect(invocation.cmd).toBe("/usr/local/bin/daedalus-audit");
    expect(invocation.options.args).toEqual([
      "--identity",
      "daedalus-copilot",
      "--tool",
      "copilot_translate",
      "--args",
      JSON.stringify(toolArgs),
      "--outcome",
      "success",
      "--log-path",
      "/tmp/test-audit.jsonl",
    ]);
  } finally {
    teardown();
  }
});

Deno.test("Copilot Audit - recordAudit returns false when subprocess exits with non-zero exit code without throwing", async () => {
  setup();
  try {
    mockCommandOutputFactory = async () => ({
      code: 1,
      stdout: new Uint8Array(),
      stderr: new TextEncoder().encode("Permission denied writing log"),
    });

    const result = await recordAudit("copilot_reject", { reason: "blocked path" }, "denied");
    expect(result).toBe(false);
    expect(mockCommandInvocations.length).toBe(1);
  } finally {
    teardown();
  }
});

Deno.test("Copilot Audit - recordAudit catches subprocess exceptions and returns false gracefully without throwing", async () => {
  setup();
  try {
    mockCommandOutputFactory = async () => {
      throw new Error("Failed to spawn subprocess: executable not found");
    };

    const result = await recordAudit("copilot_error", { error: "fatal" }, "error");
    expect(result).toBe(false);
    expect(mockCommandInvocations.length).toBe(1);
  } finally {
    teardown();
  }
});

Deno.test("Copilot Audit - recordAudit handles all 6 allowlisted copilot tool types", async () => {
  setup();
  try {
    const tools = [
      "copilot_translate",
      "copilot_reject",
      "copilot_confirm",
      "copilot_edit",
      "copilot_cancel",
      "copilot_error",
    ] as const;

    for (const t of tools) {
      const res = await recordAudit(t, { item: t }, "success");
      expect(res).toBe(true);
    }
    expect(mockCommandInvocations.length).toBe(6);
  } finally {
    teardown();
  }
});
