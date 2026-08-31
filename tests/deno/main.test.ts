import { expect } from "jsr:@std/expect@1";
import { defaultReadStdinAll, runCopilot, parseArgs, VERSION } from "../../daedalus/plugin/copilot/main.ts";

// 锁定 locale 为 en_US：本测试文件的断言基于 en_US 文案硬编码。
// 如不锁定，在 zh_CN locale 的开发机上（LC_ALL/LANG 为 zh_CN.UTF-8）
// runCopilot 内 initI18n() 会加载中文文案，断言会全部失配。
// Deno.env 设置放在模块顶层（import 之后、任何 runCopilot 调用之前），
// 因为 Deno 测试文件在执行任何 test body 前就已完成模块求值。
if ((globalThis as any).Deno?.env?.set) {
  Deno.env.set("LC_ALL", "en_US.UTF-8");
} else {
  process.env.LC_ALL = "en_US.UTF-8";
}

let stdoutChunks: string[] = [];
let stderrChunks: string[] = [];
let auditLogs: Array<{ tool: string; args: any; outcome: string }> = [];

const mockStdout = {
  write: (text: string) => {
    stdoutChunks.push(text);
  },
};

const mockStderr = {
  write: (text: string) => {
    stderrChunks.push(text);
  },
};

const mockRecordAudit = async (tool: string, args: any, outcome: string) => {
  auditLogs.push({ tool, args, outcome });
  return true;
};

function setup() {
  stdoutChunks = [];
  stderrChunks = [];
  auditLogs = [];
}

Deno.test("Copilot Main - parseArgs parses short and long flags correctly", () => {
  const parsed1 = parseArgs(["-y", "--provider", "anthropic", "--model", "claude-3-5", "--base-url", "https://api.anthropic.com", "check", "disk"]);
  expect(parsed1.yes).toBe(true);
  expect(parsed1.provider).toBe("anthropic");
  expect(parsed1.model).toBe("claude-3-5");
  expect(parsed1.baseUrl).toBe("https://api.anthropic.com");
  expect(parsed1.query).toBe("check disk");
  expect(parsed1.help).toBe(false);
  expect(parsed1.version).toBe(false);

  const parsed2 = parseArgs(["--yes", "-p", "openai", "-m", "gpt-4o", "--base-url=https://custom/v1", "show", "uptime"]);
  expect(parsed2.yes).toBe(true);
  expect(parsed2.provider).toBe("openai");
  expect(parsed2.model).toBe("gpt-4o");
  expect(parsed2.baseUrl).toBe("https://custom/v1");
  expect(parsed2.query).toBe("show uptime");

  const parsed3 = parseArgs(["-h"]);
  expect(parsed3.help).toBe(true);

  // 新语义：-V / --version 是版本旗标,与 verbose 无关;
  // parseArgs 默认 verbose=true,-v 是切换(toggle)语义。
  const parsed4 = parseArgs(["-V"]);
  expect(parsed4.version).toBe(true);
  expect(parsed4.verbose).toBe(true);

  const parsed5 = parseArgs(["-v", "-i", "--dry-run", "list", "files"]);
  // pivot 后语义:-v 是 toggle,默认 verbose=true,显式 -v 翻转为 false
  expect(parsed5.verbose).toBe(false);
  expect(parsed5.interactive).toBe(true);
  expect(parsed5.dryRun).toBe(true);
  expect(parsed5.version).toBe(false);
  expect(parsed5.query).toBe("list files");

  // --yes 保留为向后兼容别名;verbose 默认开(未传 -v)
  const parsed6 = parseArgs(["--yes"]);
  expect(parsed6.yes).toBe(true);
  expect(parsed6.interactive).toBe(false);
  expect(parsed6.verbose).toBe(true);
  expect(parsed6.dryRun).toBe(false);

  const parsed7 = parseArgs(["--verbose", "--interactive"]);
  // --verbose 与 -v 同为 toggle:默认 true → 翻转为 false
  expect(parsed7.verbose).toBe(false);
  expect(parsed7.interactive).toBe(true);

  // 默认(无任何 verbose 旗标)verbose=true;-v 双次切换回归 true
  const parsed8 = parseArgs(["check disk"]);
  expect(parsed8.verbose).toBe(true);
  const parsed9 = parseArgs(["-v", "-v", "check disk"]);
  expect(parsed9.verbose).toBe(true);
});

Deno.test("Copilot Main - parseArgs handles double-dash -- delimiter for queries", () => {
  const parsed = parseArgs(["-y", "--", "--provider", "is", "a", "flag"]);
  expect(parsed.yes).toBe(true);
  expect(parsed.query).toBe("--provider is a flag");
});

Deno.test("Copilot Main - prints help message and returns 0 on --help", async () => {
  setup();
  const code = await runCopilot({
    args: ["--help"],
    stdout: mockStdout,
    stderr: mockStderr,
  });

  expect(code).toBe(0);
  expect(stdoutChunks.join("")).toContain("Usage:");
  expect(stdoutChunks.join("")).toContain("daedalus [options]");
  // 帮助信息必须覆盖新版标志集
  const helpText = stdoutChunks.join("");
  expect(helpText).toContain("-v, --verbose");
  expect(helpText).toContain("-i, --interactive");
  expect(helpText).toContain("--dry-run");
  expect(helpText).toContain("-V, --version");
});

Deno.test("Copilot Main - prints version and returns 0 on -V / --version", async () => {
  setup();
  const code = await runCopilot({
    args: ["-V"],
    stdout: mockStdout,
    stderr: mockStderr,
  });

  expect(code).toBe(0);
  expect(stdoutChunks.join("")).toContain(`daedalus-copilot ${VERSION}`);

  setup();
  const code2 = await runCopilot({
    args: ["--version"],
    stdout: mockStdout,
    stderr: mockStderr,
  });
  expect(code2).toBe(0);
  expect(stdoutChunks.join("")).toContain(`daedalus-copilot ${VERSION}`);
});

Deno.test("Copilot Main - translates query, shows proposal, confirms with 'y', and executes via execAllowlisted", async () => {
  setup();
  let executedCommand = "";
  let executedArgs: string[] = [];

  const mockTranslate = async (_query: string) => {
    return JSON.stringify({
      command: "df",
      args: ["-h"],
      explanation: "Show disk usage",
    });
  };

  const mockExec = async (cmd: string, args?: string[]) => {
    executedCommand = cmd;
    executedArgs = args ?? [];
    return {
      stdout: "Filesystem      Size  Used Avail Use% Mounted on\n/dev/sda1        50G   20G   30G  40% /\n",
      stderr: "",
      returncode: 0,
      error: null,
    };
  };

  const inputs = ["y"];
  const mockStdinReader = async (_prompt?: string) => inputs.shift() ?? null;

  const code = await runCopilot({
    query: "check disk space",
    isTerminal: true,
    interactive: true, // 显式要求交互确认以走 y/e/n/q 循环
    stdout: mockStdout,
    stderr: mockStderr,
    stdinReader: mockStdinReader,
    translateFn: mockTranslate,
    execFn: mockExec,
    recordAuditFn: mockRecordAudit,
    readConfigFn: () => ({
      provider: "openai",
      apiKey: "test-key",
      model: "gpt-4o-mini",
      baseUrl: "https://api.openai.com/v1",
    }),
  });

  expect(code).toBe(0);
  expect(executedCommand).toBe("df");
  expect(executedArgs).toEqual(["-h"]);

  const allStdout = stdoutChunks.join("");
  expect(allStdout).toContain("Request: check disk space");
  expect(allStdout).toContain("Proposed: df -h");
  expect(allStdout).toContain("Explanation: Show disk usage");
  expect(allStdout).toContain("[privacy] This request and proposal were sent to openai (cloud LLM).");
  expect(allStdout).toContain("Filesystem      Size  Used Avail Use%");

  // 验证审计跟踪
  expect(auditLogs.some((l) => l.tool === "copilot_translate" && l.outcome === "success")).toBe(true);
  expect(
    auditLogs.some(
      (l) =>
        l.tool === "copilot_confirm" &&
        l.outcome === "success" &&
        l.args.command === "df" &&
        l.args.auto === false,
    ),
  ).toBe(true);
});

// pivot 后语义:白名单外命令走展示路径——banner + 手动提示,exit 0,绝无执行通道
Deno.test("Copilot Main - non-allowlisted command from LLM goes to display path with exit 0, denied audit, no exec", async () => {
  setup();
  let readLineCalls = 0;
  let execCalls = 0;

  const mockStdinReader = async (_prompt?: string) => {
    readLineCalls++;
    return "y";
  };

  const mockTranslate = async () => {
    return JSON.stringify({
      command: "rm",
      args: ["-rf", "/tmp/files"],
      explanation: "Delete files",
    });
  };

  const mockExec = async () => {
    execCalls++;
    return { stdout: "", stderr: "", returncode: 0, error: null };
  };

  const code = await runCopilot({
    query: "delete temporary files",
    isTerminal: true,
    stdout: mockStdout,
    stderr: mockStderr,
    stdinReader: mockStdinReader,
    translateFn: mockTranslate,
    execFn: mockExec,
    recordAuditFn: mockRecordAudit,
    readConfigFn: () => ({
      provider: "openai",
      apiKey: "test-key",
      model: "gpt-4o-mini",
      baseUrl: "https://api.openai.com/v1",
    }),
  });

  // pivot 后语义:L1/L2 走展示路径 exit 0,无 y/n 提示,无执行
  expect(code).toBe(0);
  expect(readLineCalls).toBe(0);
  expect(execCalls).toBe(0);

  // 展示路径输出:rm -rf 命中 L2 → 🚨 banner + 命令行 + 原因行 + 手动提示
  const allStdout = stdoutChunks.join("");
  expect(allStdout).toContain("🚨");
  expect(allStdout).toContain("→ rm -rf /tmp/files");
  expect(allStdout).toContain("Delete files");
  expect(allStdout).toContain("Please run this command in your terminal");
  expect(allStdout).not.toContain("Proceed?");

  // 审计:copilot_reject/denied,risk=danger,reason 为 i18n key(rm_rf)
  const rejectAudit = auditLogs.find((l) => l.tool === "copilot_reject");
  expect(rejectAudit).toBeDefined();
  expect(rejectAudit?.outcome).toBe("denied");
  expect(rejectAudit?.args.risk).toBe("danger");
  expect(rejectAudit?.args.reason).toBe("risk.pattern.rm_rf");
  expect(rejectAudit?.args.command).toBe("rm");
  expect(rejectAudit?.args.args).toEqual(["-rf", "/tmp/files"]);
});

Deno.test("Copilot Main - blocked sensitive path proposal still routes through L0 y/n confirm (cat /etc/shadow)", async () => {
  // 行为对齐说明:main.ts pivot 后主流程不再调用 validateProposal,
  // 仅 classifyProposal 定级。cat 在 15 命令白名单且无 L2 模式命中,
  // 故 cat /etc/shadow 分级 safe + L0 白名单内 → 走 TTY y/n 确认路径,
  // 敏感路径防线由 daedalus-shell 沙箱网关承担(validateArg)。
  // 本用例钉住该真实行为:读 stdin 一次,用户 "n" 拒绝 → 无执行,exit 0。
  setup();
  let readLineCalls = 0;
  let execCalls = 0;

  const prompts: string[] = [];
  const inputs = ["n"];
  const mockStdinReader = async (prompt?: string) => {
    readLineCalls++;
    prompts.push(prompt ?? "");
    return inputs.shift() ?? null;
  };

  const mockTranslate = async () => {
    return JSON.stringify({
      command: "cat",
      args: ["/etc/shadow"],
      explanation: "Read shadow file",
    });
  };

  const mockExec = async () => {
    execCalls++;
    return { stdout: "root:$6$...\n", stderr: "", returncode: 0, error: null };
  };

  const code = await runCopilot({
    query: "read password hashes",
    isTerminal: true,
    stdout: mockStdout,
    stderr: mockStderr,
    stdinReader: mockStdinReader,
    translateFn: mockTranslate,
    execFn: mockExec,
    recordAuditFn: mockRecordAudit,
    readConfigFn: () => ({
      provider: "openai",
      apiKey: "test-key",
      model: "gpt-4o-mini",
      baseUrl: "https://api.openai.com/v1",
    }),
  });

  // TTY 下先弹 y/n 确认;用户 "n" 后追加 feedback 提示(EOF)→ copilot_cancel,
  // 无执行,exit 0。提示语经 stdinReader 参数注入(不回显 stdout),须捕获断言。
  expect(code).toBe(0);
  expect(readLineCalls).toBe(2);
  expect(prompts[0]).toContain("Proceed?");
  expect(prompts[1]).toContain("Feedback for revision:");
  expect(execCalls).toBe(0);
  expect(stdoutChunks.join("")).not.toContain("root:$6$");
  expect(auditLogs.some((l) => l.tool === "copilot_cancel" && l.outcome === "denied")).toBe(true);
  expect(auditLogs.some((l) => l.tool === "copilot_reject")).toBe(false);
  expect(auditLogs.some((l) => l.tool === "copilot_translate" && l.args.risk_level === "safe")).toBe(true);
});

Deno.test("Copilot Main - handles invalid JSON from LLM with exit code 1 and copilot_reject denied audit", async () => {
  setup();
  let execCalled = false;

  const mockTranslate = async () => {
    return "I cannot do that as an AI assistant.";
  };

  const mockExec = async () => {
    execCalled = true;
    return { stdout: "", stderr: "", returncode: 0, error: null };
  };

  const code = await runCopilot({
    query: "do something",
    isTerminal: true,
    stdout: mockStdout,
    stderr: mockStderr,
    translateFn: mockTranslate,
    execFn: mockExec,
    recordAuditFn: mockRecordAudit,
    readConfigFn: () => ({
      provider: "openai",
      apiKey: "test-key",
      model: "gpt-4o-mini",
      baseUrl: "https://api.openai.com/v1",
    }),
  });

  expect(code).toBe(1);
  expect(execCalled).toBe(false);
  expect(stderrChunks.join("")).toContain("Security policy rejection:");
  expect(auditLogs.some((l) => l.tool === "copilot_reject" && l.outcome === "denied")).toBe(true);
});

Deno.test("Copilot Main - translate fallback: error without kind renders raw msg and audits error_kind='unknown'", async () => {
  setup();
  // 无 kind 属性的意外异常 → 兜底 t("error.translate", 原始 message) 原样透传
  const mockTranslate = async () => {
    throw new Error("OpenAI API error (500): Internal Server Error");
  };

  const code = await runCopilot({
    query: "show uptime",
    isTerminal: true,
    stdout: mockStdout,
    stderr: mockStderr,
    translateFn: mockTranslate,
    recordAuditFn: mockRecordAudit,
    readConfigFn: () => ({
      provider: "openai",
      apiKey: "test-key",
      model: "gpt-4o-mini",
      baseUrl: "https://api.openai.com/v1",
    }),
  });

  expect(code).toBe(1);
  expect(stderrChunks.join("")).toContain("OpenAI API error (500): Internal Server Error");
  const errLog = auditLogs.find((l) => l.tool === "copilot_error" && l.outcome === "error");
  expect(errLog).toBeDefined();
  expect(errLog?.args.query).toBe("show uptime");
  expect(errLog?.args.error).toBe("OpenAI API error (500): Internal Server Error");
  expect(errLog?.args.error_kind).toBe("unknown");
  // 兜底无 fields:不得出现 endpoint/timeout_ms/status 键(条件展开,JSON 无 null 噪音)
  expect("endpoint" in errLog?.args).toBe(false);
  expect("timeout_ms" in errLog?.args).toBe(false);
  expect("status" in errLog?.args).toBe(false);
});

Deno.test("Copilot Main - translate structured timeout error renders i18n text with seconds + audits endpoint/timeout_ms", async () => {
  // W1 形状: Object.assign(new Error(msg), { kind, fields })(决策 7: Error+属性非子类)
  const buildTimeoutError = (timeoutMs: number) =>
    Object.assign(
      new Error("The operation was aborted due to timeout"),
      {
        kind: "timeout",
        fields: { endpoint: "http://x/v1/chat/completions", timeoutMs },
      },
    );

  setup();
  const mockTranslate = async () => {
    throw buildTimeoutError(30000);
  };

  const baseOptions = {
    query: "show uptime",
    isTerminal: true,
    stdout: mockStdout,
    stderr: mockStderr,
    translateFn: mockTranslate,
    recordAuditFn: mockRecordAudit,
    readConfigFn: () => ({
      provider: "openai" as const,
      apiKey: "test-key",
      model: "gpt-4o-mini",
      baseUrl: "https://api.openai.com/v1",
    }),
  };

  const code = await runCopilot(baseOptions);

  expect(code).toBe(1);
  const stderr = stderrChunks.join("");
  // en_US locale 文案:端点 + 秒数换算(30000ms → 30,不是原始毫秒)
  expect(stderr).toContain("LLM request to http://x/v1/chat/completions did not respond within 30s");
  const errLog = auditLogs.find((l) => l.tool === "copilot_error" && l.outcome === "error");
  expect(errLog?.args.error_kind).toBe("timeout");
  expect(errLog?.args.endpoint).toBe("http://x/v1/chat/completions");
  expect(errLog?.args.timeout_ms).toBe(30000);
  expect(errLog?.args.error).toContain("LLM request to");
  expect("status" in (errLog?.args ?? {})).toBe(false);

  // 秒数换算规则:1000ms → "1s"(防 "0.001" 类小数回归)
  setup();
  const mockTranslateFast = async () => {
    throw buildTimeoutError(1000);
  };
  await runCopilot({ ...baseOptions, translateFn: mockTranslateFast });
  expect(stderrChunks.join("")).toContain("did not respond within 1s");
});

Deno.test("Copilot Main - translate structured http/network errors render i18n text + audits kind-specific fields", async () => {
  setup();
  const mockTranslate = async () => {
    throw Object.assign(
      new Error("OpenAI API error (400): invalid api key"),
      {
        kind: "http",
        fields: {
          endpoint: "https://api.openai.com/v1/chat/completions",
          status: 400,
          body: "invalid api key",
          timeoutMs: 30000,
        },
      },
    );
  };

  const code = await runCopilot({
    query: "show uptime",
    isTerminal: true,
    stdout: mockStdout,
    stderr: mockStderr,
    translateFn: mockTranslate,
    recordAuditFn: mockRecordAudit,
    readConfigFn: () => ({
      provider: "openai",
      apiKey: "test-key",
      model: "gpt-4o-mini",
      baseUrl: "https://api.openai.com/v1",
    }),
  });

  expect(code).toBe(1);
  expect(stderrChunks.join("")).toContain(
    "LLM endpoint https://api.openai.com/v1/chat/completions returned HTTP 400: invalid api key",
  );
  const httpLog = auditLogs.find((l) => l.tool === "copilot_error" && l.outcome === "error");
  expect(httpLog?.args.error_kind).toBe("http");
  expect(httpLog?.args.status).toBe(400);
  expect(httpLog?.args.timeout_ms).toBe(30000); // http 场景亦上报 timeout_ms
  expect(httpLog?.args.endpoint).toBe("https://api.openai.com/v1/chat/completions");

  setup();
  const mockTranslateNet = async () => {
    throw Object.assign(
      new Error("fetch failed"),
      {
        kind: "network",
        fields: { endpoint: "https://api.openai.com/v1/chat/completions", err: "Connection refused" },
      },
    );
  };

  await runCopilot({
    query: "show uptime",
    isTerminal: true,
    stdout: mockStdout,
    stderr: mockStderr,
    translateFn: mockTranslateNet,
    recordAuditFn: mockRecordAudit,
    readConfigFn: () => ({
      provider: "openai",
      apiKey: "test-key",
      model: "gpt-4o-mini",
      baseUrl: "https://api.openai.com/v1",
    }),
  });

  expect(stderrChunks.join("")).toContain(
    "Failed to reach LLM endpoint https://api.openai.com/v1/chat/completions: Connection refused",
  );
  const netLog = auditLogs.find((l) => l.tool === "copilot_error" && l.outcome === "error");
  expect(netLog?.args.error_kind).toBe("network");
  expect(netLog?.args.endpoint).toBe("https://api.openai.com/v1/chat/completions");
  // network 场景不报 timeout_ms/status(仅 timeout/http 条件展开)
  expect("timeout_ms" in (netLog?.args ?? {})).toBe(false);
  expect("status" in (netLog?.args ?? {})).toBe(false);
});

Deno.test("Copilot Main - revise path shares renderLLMError: structured timeout renders i18n text + audits error_kind", async () => {
  setup();
  const mockTranslate = async () => {
    return JSON.stringify({
      command: "free",
      args: ["-h"],
      explanation: "Show memory",
    });
  };

  // revise 与 translate 同走 callProvider,抛同款结构化错误
  const mockRevise = async () => {
    throw Object.assign(
      new Error("The operation was aborted due to timeout"),
      { kind: "timeout", fields: { endpoint: "http://revise/v1/chat/completions", timeoutMs: 2500 } },
    );
  };

  // 交互流程: 提议 'free -h' -> 'n' -> 反馈 -> revise 抛错
  const inputs = ["n", "show in MB"];
  const mockStdinReader = async () => inputs.shift() ?? null;

  const code = await runCopilot({
    query: "show memory",
    isTerminal: true,
    interactive: true,
    stdout: mockStdout,
    stderr: mockStderr,
    stdinReader: mockStdinReader,
    translateFn: mockTranslate,
    reviseFn: mockRevise,
    recordAuditFn: mockRecordAudit,
    readConfigFn: () => ({
      provider: "openai",
      apiKey: "test-key",
      model: "gpt-4o-mini",
      baseUrl: "https://api.openai.com/v1",
    }),
  });

  expect(code).toBe(1);
  // 2500ms → Math.round = 3(展示走同一 helper,含秒换算)
  expect(stderrChunks.join("")).toContain(
    "LLM request to http://revise/v1/chat/completions did not respond within 3s",
  );
  const errLog = auditLogs.find((l) => l.tool === "copilot_error" && l.outcome === "error");
  expect(errLog?.args.error_kind).toBe("timeout");
  expect(errLog?.args.timeout_ms).toBe(2500);
  expect(errLog?.args.endpoint).toBe("http://revise/v1/chat/completions");
});

Deno.test("Copilot Main - allows user to manually edit proposed command without invoking LLM, re-validates, and executes", async () => {
  setup();
  let translateCount = 0;
  let executedCommand = "";
  let executedArgs: string[] = [];

  const mockTranslate = async () => {
    translateCount++;
    return JSON.stringify({
      command: "df",
      args: ["-h"],
      explanation: "Show all disk usage",
    });
  };

  // 交互流程: 提议 'df -h' -> 用户选择 'e' -> 输入 'df -h /tmp' -> 重新显示 -> 用户输入 'y'
  const inputs = ["e", "df -h /tmp", "y"];
  const mockStdinReader = async () => inputs.shift() ?? null;

  const mockExec = async (cmd: string, args?: string[]) => {
    executedCommand = cmd;
    executedArgs = args ?? [];
    return { stdout: "/tmp 100M\n", stderr: "", returncode: 0, error: null };
  };

  const code = await runCopilot({
    query: "check tmp space",
    isTerminal: true,
    interactive: true, // 走 [e]dit 编辑路径需要交互确认循环
    stdout: mockStdout,
    stderr: mockStderr,
    stdinReader: mockStdinReader,
    translateFn: mockTranslate,
    execFn: mockExec,
    recordAuditFn: mockRecordAudit,
    readConfigFn: () => ({
      provider: "openai",
      apiKey: "test-key",
      model: "gpt-4o-mini",
      baseUrl: "https://api.openai.com/v1",
    }),
  });

  expect(code).toBe(0);
  expect(translateCount).toBe(1); // 仅调用一次 translate（编辑命令时不调用 LLM）
  expect(executedCommand).toBe("df");
  expect(executedArgs).toEqual(["-h", "/tmp"]);

  // 验证审计日志
  const editAudit = auditLogs.find((l) => l.tool === "copilot_edit");
  expect(editAudit).toBeDefined();
  expect(editAudit?.outcome).toBe("success");
  expect(editAudit?.args.edited.args).toEqual(["-h", "/tmp"]);

  const confirmAudit = auditLogs.find((l) => l.tool === "copilot_confirm");
  expect(confirmAudit?.args.command).toBe("df");
  expect(confirmAudit?.args.args).toEqual(["-h", "/tmp"]);
});

Deno.test("Copilot Main - rejects invalid user edit, logs copilot_reject, and allows subsequent valid input", async () => {
  setup();
  const mockTranslate = async () => {
    return JSON.stringify({
      command: "df",
      args: ["-h"],
      explanation: "Show disk usage",
    });
  };

  let executedCommand = "";
  const mockExec = async (cmd: string, _args?: string[]) => {
    executedCommand = cmd;
    return { stdout: "ok\n", stderr: "", returncode: 0, error: null };
  };

  // 交互流程: 提议 'df -h' -> 用户输入 'e' -> 尝试 'rm -rf /' (被拒绝) -> 在原始提议上输入 'y'
  const inputs = ["e", "rm -rf /", "y"];
  const mockStdinReader = async () => inputs.shift() ?? null;

  const code = await runCopilot({
    query: "clean disk",
    isTerminal: true,
    interactive: true, // 走编辑 + 重新验证路径
    stdout: mockStdout,
    stderr: mockStderr,
    stdinReader: mockStdinReader,
    translateFn: mockTranslate,
    execFn: mockExec,
    recordAuditFn: mockRecordAudit,
    readConfigFn: () => ({
      provider: "openai",
      apiKey: "test-key",
      model: "gpt-4o-mini",
      baseUrl: "https://api.openai.com/v1",
    }),
  });

  expect(code).toBe(0);
  expect(executedCommand).toBe("df");
  expect(stderrChunks.join("")).toContain("Security policy rejection:");
  expect(stderrChunks.join("")).toContain("not in ALLOW_COMMANDS");
  expect(auditLogs.some((l) => l.tool === "copilot_reject" && l.outcome === "denied")).toBe(true);
});

Deno.test("Copilot Main - handles feedback, calls revise(), validates revised proposal, and executes on confirmation", async () => {
  setup();
  let reviseCalled = false;
  let capturedFeedback = "";

  const mockTranslate = async () => {
    return JSON.stringify({
      command: "free",
      args: ["-h"],
      explanation: "Show memory in human readable format",
    });
  };

  const mockRevise = async (_history: any[], feedback: string) => {
    reviseCalled = true;
    capturedFeedback = feedback;
    return JSON.stringify({
      command: "free",
      args: ["-m"],
      explanation: "Show memory in megabytes",
    });
  };

  let executedArgs: string[] = [];
  const mockExec = async (_cmd: string, args?: string[]) => {
    executedArgs = args ?? [];
    return { stdout: "Mem: 16000 MB\n", stderr: "", returncode: 0, error: null };
  };

  // 交互流程: 提议 'free -h' -> 用户输入 'n' -> 反馈 'show in MB' -> 修改为 'free -m' -> 用户输入 'y'
  const inputs = ["n", "show in MB", "y"];
  const mockStdinReader = async () => inputs.shift() ?? null;

  const code = await runCopilot({
    query: "show memory",
    isTerminal: true,
    interactive: true, // 走 [n]o 反馈修订路径
    stdout: mockStdout,
    stderr: mockStderr,
    stdinReader: mockStdinReader,
    translateFn: mockTranslate,
    reviseFn: mockRevise,
    execFn: mockExec,
    recordAuditFn: mockRecordAudit,
    readConfigFn: () => ({
      provider: "openai",
      apiKey: "test-key",
      model: "gpt-4o-mini",
      baseUrl: "https://api.openai.com/v1",
    }),
  });

  expect(code).toBe(0);
  expect(reviseCalled).toBe(true);
  expect(capturedFeedback).toBe("show in MB");
  expect(executedArgs).toEqual(["-m"]);
});

Deno.test("Copilot Main - enforces revision limit of 3 rounds and exits with code 1 and error message", async () => {
  setup();
  let reviseCount = 0;

  const mockTranslate = async () => {
    return JSON.stringify({
      command: "uptime",
      args: [],
      explanation: "Show uptime",
    });
  };

  const mockRevise = async () => {
    reviseCount++;
    return JSON.stringify({
      command: "uptime",
      args: [],
      explanation: `Show uptime revision ${reviseCount}`,
    });
  };

  // 用户尝试连续 4 次 'n' 反馈（超过 3 次限制）
  const inputs = [
    "n", "revision 1",
    "n", "revision 2",
    "n", "revision 3",
    "n", // 第 4 次尝试触发限制
  ];
  const mockStdinReader = async () => inputs.shift() ?? null;

  const code = await runCopilot({
    query: "system status",
    isTerminal: true,
    interactive: true, // 走多轮反馈以触发修订上限
    stdout: mockStdout,
    stderr: mockStderr,
    stdinReader: mockStdinReader,
    translateFn: mockTranslate,
    reviseFn: mockRevise,
    recordAuditFn: mockRecordAudit,
    readConfigFn: () => ({
      provider: "openai",
      apiKey: "test-key",
      model: "gpt-4o-mini",
      baseUrl: "https://api.openai.com/v1",
    }),
  });

  expect(code).toBe(1);
  expect(reviseCount).toBe(3);
  expect(stderrChunks.join("")).toContain("Revision limit reached (3).");
  expect(
    auditLogs.some(
      (l) =>
        l.tool === "copilot_reject" &&
        l.outcome === "denied" &&
        l.args.reason === "revision limit reached",
    ),
  ).toBe(true);
});

Deno.test("Copilot Main - exits with code 0 and logs copilot_cancel when user selects 'q'", async () => {
  setup();
  const mockTranslate = async () => {
    return JSON.stringify({
      command: "hostname",
      args: [],
      explanation: "Show hostname",
    });
  };

  let execCalled = false;
  const mockExec = async () => {
    execCalled = true;
    return { stdout: "", stderr: "", returncode: 0, error: null };
  };

  const inputs = ["q"];
  const mockStdinReader = async () => inputs.shift() ?? null;

  const code = await runCopilot({
    query: "what is my host",
    isTerminal: true,
    interactive: true, // 走 [q]uit 取消路径
    stdout: mockStdout,
    stderr: mockStderr,
    stdinReader: mockStdinReader,
    translateFn: mockTranslate,
    execFn: mockExec,
    recordAuditFn: mockRecordAudit,
    readConfigFn: () => ({
      provider: "openai",
      apiKey: "test-key",
      model: "gpt-4o-mini",
      baseUrl: "https://api.openai.com/v1",
    }),
  });

  expect(code).toBe(0);
  expect(execCalled).toBe(false);
  expect(auditLogs.some((l) => l.tool === "copilot_cancel" && l.outcome === "denied")).toBe(true);
});

Deno.test("Copilot Main - handles EOF (Ctrl-D) gracefully by logging copilot_cancel and exiting 0", async () => {
  setup();
  const mockTranslate = async () => {
    return JSON.stringify({
      command: "whoami",
      args: [],
      explanation: "Show user",
    });
  };

  const mockStdinReader = async () => null; // 模拟 EOF

  const code = await runCopilot({
    query: "who am i",
    isTerminal: true,
    interactive: true, // EOF 取消仅在交互确认循环中触发
    stdout: mockStdout,
    stderr: mockStderr,
    stdinReader: mockStdinReader,
    translateFn: mockTranslate,
    recordAuditFn: mockRecordAudit,
    readConfigFn: () => ({
      provider: "openai",
      apiKey: "test-key",
      model: "gpt-4o-mini",
      baseUrl: "https://api.openai.com/v1",
    }),
  });

  expect(code).toBe(0);
  expect(auditLogs.some((l) => l.tool === "copilot_cancel" && l.outcome === "denied")).toBe(true);
});

// 新语义:非 TTY 管道环境下仅沙箱白名单内只读诊断直接运行,其余只展示;不要求 --yes
Deno.test("Copilot Main - whitelisted read-only diagnostics run directly in non-TTY without requiring --yes", async () => {
  setup();
  let executedCommand = "";
  let executedArgs: string[] = [];

  const mockTranslate = async () => {
    return JSON.stringify({
      command: "df",
      args: ["-h"],
      explanation: "Show disk usage",
    });
  };

  const mockExec = async (cmd: string, args?: string[]) => {
    executedCommand = cmd;
    executedArgs = args ?? [];
    return { stdout: "ok\n", stderr: "", returncode: 0, error: null };
  };

  const code = await runCopilot({
    query: "check disk",
    isTerminal: false,
    stdout: mockStdout,
    stderr: mockStderr,
    translateFn: mockTranslate,
    execFn: mockExec,
    recordAuditFn: mockRecordAudit,
    readConfigFn: () => ({
      provider: "openai",
      apiKey: "test-key",
      model: "gpt-4o-mini",
      baseUrl: "https://api.openai.com/v1",
    }),
  });

  expect(code).toBe(0);
  expect(executedCommand).toBe("df");
  expect(executedArgs).toEqual(["-h"]);
  expect(stdoutChunks.join("")).toContain("ok");
  expect(
    auditLogs.some(
      (l) => l.tool === "copilot_confirm" && l.outcome === "success" && l.args.auto === true,
    ),
  ).toBe(true);
});

// 新语义：-i 与非 TTY 组合是唯一被拒绝的情况
Deno.test("Copilot Main - fails with exit 1 in non-TTY when -i is requested, without translating or executing", async () => {
  setup();
  let translateCalled = false;
  let execCalled = false;

  const mockTranslate = async () => {
    translateCalled = true;
    return "{}";
  };

  const mockExec = async () => {
    execCalled = true;
    return { stdout: "", stderr: "", returncode: 0, error: null };
  };

  const code = await runCopilot({
    args: ["-i", "check disk"],
    isTerminal: false,
    stdout: mockStdout,
    stderr: mockStderr,
    translateFn: mockTranslate,
    execFn: mockExec,
    recordAuditFn: mockRecordAudit,
    readConfigFn: () => ({
      provider: "openai",
      apiKey: "test-key",
      model: "gpt-4o-mini",
      baseUrl: "https://api.openai.com/v1",
    }),
  });

  expect(code).toBe(1);
  expect(translateCalled).toBe(false); // 在进入 LLM 转换之前就被拦截
  expect(execCalled).toBe(false);
  expect(stderrChunks.join("")).toContain(
    "Interactive confirmation (-i) needs a terminal. In non-TTY contexts only whitelisted read-only diagnostics run; everything else is shown for manual execution.",
  );
});

// 新语义：--dry-run 仅打印将要执行的命令，绝不执行
Deno.test("Copilot Main - --dry-run prints the would-execute command without executing and exits 0", async () => {
  setup();
  let execCalled = false;

  const mockTranslate = async () => {
    return JSON.stringify({
      command: "df",
      args: ["-h"],
      explanation: "Show disk usage",
    });
  };

  const mockExec = async () => {
    execCalled = true;
    return { stdout: "", stderr: "", returncode: 0, error: null };
  };

  const code = await runCopilot({
    args: ["--dry-run", "check disk"],
    isTerminal: true,
    stdout: mockStdout,
    stderr: mockStderr,
    translateFn: mockTranslate,
    execFn: mockExec,
    recordAuditFn: mockRecordAudit,
    readConfigFn: () => ({
      provider: "openai",
      apiKey: "test-key",
      model: "gpt-4o-mini",
      baseUrl: "https://api.openai.com/v1",
    }),
  });

  expect(code).toBe(0);
  expect(execCalled).toBe(false);
  expect(stdoutChunks.join("")).toContain("[dry-run] Proposed command (not run): df -h");
  expect(
    auditLogs.some(
      (l) => l.tool === "copilot_confirm" && l.outcome === "success" && l.args.mode === "dry-run",
    ),
  ).toBe(true);
});

// pivot 后语义:TTY 默认 y/n 确认(读 stdin 一次);"n" 拒绝 → 无执行,exit 0
Deno.test("Copilot Main - TTY default prompts y/n once; 'n' declines execution and exits 0", async () => {
  setup();
  let readLineCalls = 0;
  let execCalls = 0;
  const prompts: string[] = [];
  const inputs = ["n"];
  const mockStdinReader = async (prompt?: string) => {
    readLineCalls++;
    prompts.push(prompt ?? "");
    return inputs.shift() ?? null;
  };

  const mockTranslate = async () => {
    return JSON.stringify({
      command: "df",
      args: ["-h"],
      explanation: "Show disk usage",
    });
  };

  const mockExec = async (_cmd: string) => {
    execCalls++;
    return { stdout: "fs-table\n", stderr: "", returncode: 0, error: null };
  };

  const code = await runCopilot({
    args: ["check disk"],
    isTerminal: true,
    stdout: mockStdout,
    stderr: mockStderr,
    stdinReader: mockStdinReader,
    translateFn: mockTranslate,
    execFn: mockExec,
    recordAuditFn: mockRecordAudit,
    readConfigFn: () => ({
      provider: "openai",
      apiKey: "test-key",
      model: "gpt-4o-mini",
      baseUrl: "https://api.openai.com/v1",
    }),
  });

  expect(code).toBe(0);
  expect(readLineCalls).toBe(2); // y/n 一次 + "n" 后的 feedback 提示(EOF 取消)
  expect(prompts[0]).toContain("Proceed?"); // 提示语经 stdinReader 参数注入,不回显 stdout
  expect(prompts[1]).toContain("Feedback for revision:");
  expect(execCalls).toBe(0); // "n" 拒绝 → 无执行
  expect(stdoutChunks.join("")).not.toContain("fs-table");
  // 拒绝路径落 copilot_cancel;confirm/translate 的 success 链仍完整
  expect(auditLogs.some((l) => l.tool === "copilot_cancel" && l.outcome === "denied")).toBe(true);
  expect(auditLogs.some((l) => l.tool === "copilot_translate" && l.outcome === "success" && l.args.risk_level === "safe")).toBe(true);
});

// pivot 后语义:verbose 默认开启(先打印翻译预览),TTY 下读一次 stdin;
// "y" 确认 → 执行
Deno.test("Copilot Main - verbose prints translated command and explanation, prompts once, executes on 'y'", async () => {
  setup();
  let readLineCalls = 0;
  let executedCommand = "";

  const prompts: string[] = [];
  const mockStdinReader = async (prompt?: string) => {
    readLineCalls++;
    prompts.push(prompt ?? "");
    return "y";
  };

  const mockTranslate = async () => {
    return JSON.stringify({
      command: "df",
      args: ["-h"],
      explanation: "Show disk usage",
    });
  };

  const mockExec = async (cmd: string) => {
    executedCommand = cmd;
    return { stdout: "executed\n", stderr: "", returncode: 0, error: null };
  };

  const code = await runCopilot({
    args: ["check disk"],
    isTerminal: true,
    stdout: mockStdout,
    stderr: mockStderr,
    stdinReader: mockStdinReader,
    translateFn: mockTranslate,
    execFn: mockExec,
    recordAuditFn: mockRecordAudit,
    readConfigFn: () => ({
      provider: "openai",
      apiKey: "test-key",
      model: "gpt-4o-mini",
      baseUrl: "https://api.openai.com/v1",
    }),
  });

  expect(code).toBe(0);
  expect(executedCommand).toBe("df");
  expect(readLineCalls).toBe(1); // 默认(TTY 无旗标)读一次 stdin
  expect(prompts[0]).toContain("Proceed?"); // 确认提示经 stdinReader 参数注入
  const allStdout = stdoutChunks.join("");
  // verbose 默认开:执行前打印 → cmd + explanation
  expect(allStdout).toContain("→ df -h");
  expect(allStdout).toContain("Show disk usage");
  expect(allStdout).toContain("executed");
  // verbose 输出必须出现在执行结果之前
  expect(allStdout.indexOf("→ df -h")).toBeLessThan(allStdout.indexOf("executed"));
  // "y" 显式确认 → auto:false,且带 pivot 新增 risk_level 审计字段
  expect(
    auditLogs.some(
      (l) =>
        l.tool === "copilot_confirm" &&
        l.outcome === "success" &&
        l.args.auto === false &&
        l.args.risk_level === "safe",
    ),
  ).toBe(true);
});

Deno.test("Copilot Main - auto-confirms and executes when --yes is provided in non-TTY environment", async () => {
  setup();
  let executedCommand = "";
  let executedArgs: string[] = [];

  const mockTranslate = async () => {
    return JSON.stringify({
      command: "df",
      args: ["-h", "/var/log"],
      explanation: "Check /var/log disk usage",
    });
  };

  const mockExec = async (cmd: string, args?: string[]) => {
    executedCommand = cmd;
    executedArgs = args ?? [];
    return {
      stdout: "/var/log 500M\n",
      stderr: "",
      returncode: 0,
      error: null,
    };
  };

  const code = await runCopilot({
    query: "check log disk",
    isTerminal: false,
    yes: true,
    stdout: mockStdout,
    stderr: mockStderr,
    translateFn: mockTranslate,
    execFn: mockExec,
    recordAuditFn: mockRecordAudit,
    readConfigFn: () => ({
      provider: "openai",
      apiKey: "test-key",
      model: "gpt-4o-mini",
      baseUrl: "https://api.openai.com/v1",
    }),
  });

  expect(code).toBe(0);
  expect(executedCommand).toBe("df");
  expect(executedArgs).toEqual(["-h", "/var/log"]);
  expect(stdoutChunks.join("")).toContain("/var/log 500M");

  expect(
    auditLogs.some(
      (l) =>
        l.tool === "copilot_confirm" &&
        l.outcome === "success" &&
        l.args.auto === true,
    ),
  ).toBe(true);
});

Deno.test("Copilot Main - auto-confirms and executes when --yes is provided in TTY environment without prompting", async () => {
  setup();
  let promptCalled = false;
  const mockStdinReader = async () => {
    promptCalled = true;
    return "y";
  };

  const mockTranslate = async () => {
    return JSON.stringify({
      command: "uname",
      args: ["-a"],
      explanation: "Show kernel info",
    });
  };

  const mockExec = async () => {
    return {
      stdout: "Linux daedalus 6.8.0 #1 SMP\n",
      stderr: "",
      returncode: 0,
      error: null,
    };
  };

  const code = await runCopilot({
    args: ["--yes", "show kernel"],
    isTerminal: true,
    stdout: mockStdout,
    stderr: mockStderr,
    stdinReader: mockStdinReader,
    translateFn: mockTranslate,
    execFn: mockExec,
    recordAuditFn: mockRecordAudit,
    readConfigFn: () => ({
      provider: "openai",
      apiKey: "test-key",
      model: "gpt-4o-mini",
      baseUrl: "https://api.openai.com/v1",
    }),
  });

  expect(code).toBe(0);
  expect(promptCalled).toBe(false); // 从未通过标准输入提示用户
  expect(stdoutChunks.join("")).toContain("Linux daedalus");
});

Deno.test("Copilot Main - passes CLI flags --provider, --model, --base-url to readConfig overrides", async () => {
  setup();
  let passedOverrides: any = null;

  const mockReadConfig = (overrides?: any) => {
    passedOverrides = overrides;
    return {
      provider: "anthropic" as const,
      apiKey: "test-ant-key",
      model: "claude-3-5-sonnet",
      baseUrl: "https://custom.anthropic.endpoint",
    };
  };

  const mockTranslate = async () => {
    return JSON.stringify({
      command: "date",
      args: [],
      explanation: "Show date",
    });
  };

  const mockExec = async () => ({
    stdout: "Thu Aug 27 2026\n",
    stderr: "",
    returncode: 0,
    error: null,
  });

  const code = await runCopilot({
    args: [
      "--provider", "anthropic",
      "--model", "claude-3-5-sonnet",
      "--base-url", "https://custom.anthropic.endpoint",
      "-y",
      "current date",
    ],
    isTerminal: true,
    stdout: mockStdout,
    stderr: mockStderr,
    translateFn: mockTranslate,
    execFn: mockExec,
    recordAuditFn: mockRecordAudit,
    readConfigFn: mockReadConfig,
  });

  expect(code).toBe(0);
  expect(passedOverrides).toEqual({
    provider: "anthropic",
    model: "claude-3-5-sonnet",
    baseUrl: "https://custom.anthropic.endpoint",
  });
  expect(stdoutChunks.join("")).toContain("Thu Aug 27 2026");
});

Deno.test("Copilot Main - runs REPL loop, resets revision counter independently per query, and exits on 'exit'", async () => {
  setup();
  let translateQueries: string[] = [];

  const mockTranslate = async (query: string) => {
    translateQueries.push(query);
    if (query === "query1") {
      return JSON.stringify({
        command: "pwd",
        args: [],
        explanation: "Print working directory",
      });
    }
    return JSON.stringify({
      command: "arch",
      args: [],
      explanation: "Print machine architecture",
    });
  };

  const mockExec = async () => ({
    stdout: "result\n",
    stderr: "",
    returncode: 0,
    error: null,
  });

  // 交互流程:
  // 提示 1 (REPL): "query1" -> 确认 'y'
  // 提示 2 (REPL): "query2" -> 确认 'y'
  // 提示 3 (REPL): "exit"
  const inputs = [
    "query1",
    "y",
    "query2",
    "y",
    "exit",
  ];
  const mockStdinReader = async (_prompt?: string) => inputs.shift() ?? null;

  const code = await runCopilot({
    args: [], // 无查询参数 -> REPL 模式
    isTerminal: true,
    interactive: true, // REPL 内重放 y 确认序列，需要交互确认循环
    stdout: mockStdout,
    stderr: mockStderr,
    stdinReader: mockStdinReader,
    translateFn: mockTranslate,
    execFn: mockExec,
    recordAuditFn: mockRecordAudit,
    readConfigFn: () => ({
      provider: "openai",
      apiKey: "test-key",
      model: "gpt-4o-mini",
      baseUrl: "https://api.openai.com/v1",
    }),
  });

  expect(code).toBe(0);
  expect(translateQueries).toEqual(["query1", "query2"]);
});

// 新增:L1/L2 展示路径断言(pivot 任务 5)
// 展示路径契约:无 y/n 提示(readLineCalls===0)、无 exec、copilot_reject/denied
// 携带 risk 档位,stdout 有分级 banner + 手动提示。
Deno.test("Copilot Main - L1 (git push) display path: banner, no prompt, no exec, copilot_reject risk=caution", async () => {
  setup();
  let readLineCalls = 0;
  let execCalls = 0;

  const mockStdinReader = async (_prompt?: string) => {
    readLineCalls++;
    return "y";
  };

  const mockTranslate = async () => {
    return JSON.stringify({
      command: "git",
      args: ["push", "origin", "main"],
      explanation: "Push commits to remote",
    });
  };

  const mockExec = async () => {
    execCalls++;
    return { stdout: "", stderr: "", returncode: 0, error: null };
  };

  const code = await runCopilot({
    query: "push my commits",
    isTerminal: true,
    stdout: mockStdout,
    stderr: mockStderr,
    stdinReader: mockStdinReader,
    translateFn: mockTranslate,
    execFn: mockExec,
    recordAuditFn: mockRecordAudit,
    readConfigFn: () => ({
      provider: "openai",
      apiKey: "test-key",
      model: "gpt-4o-mini",
      baseUrl: "https://api.openai.com/v1",
    }),
  });

  // pivot 后语义:L1/L2 走展示路径 exit 0,无 y/n 提示,无执行
  expect(code).toBe(0);
  expect(readLineCalls).toBe(0);
  expect(execCalls).toBe(0);

  // ⚠ banner + 命令行 + 手动提示;无确认提示语
  const allStdout = stdoutChunks.join("");
  expect(allStdout).toContain("⚠");
  expect(allStdout).toContain("→ git push origin main");
  expect(allStdout).toContain("Push commits to remote");
  expect(allStdout).toContain("Please run this command in your terminal");
  expect(allStdout).not.toContain("Proceed?");

  // 审计:copilot_reject/denied 携带 risk=caution 与 reason i18n key
  const rejectAudit = auditLogs.find((l) => l.tool === "copilot_reject");
  expect(rejectAudit).toBeDefined();
  expect(rejectAudit?.outcome).toBe("denied");
  expect(rejectAudit?.args.risk).toBe("caution");
  expect(rejectAudit?.args.reason).toBe("risk.reason.caution_command");
});

Deno.test("Copilot Main - L2 (rm -rf) display path: danger banner + reason line, no prompt, no exec, copilot_reject risk=danger", async () => {
  setup();
  let readLineCalls = 0;
  let execCalls = 0;

  const mockStdinReader = async (_prompt?: string) => {
    readLineCalls++;
    return "y";
  };

  const mockTranslate = async () => {
    return JSON.stringify({
      command: "rm",
      args: ["-rf", "/tmp/x"],
      explanation: "Remove directory",
    });
  };

  const mockExec = async () => {
    execCalls++;
    return { stdout: "", stderr: "", returncode: 0, error: null };
  };

  const code = await runCopilot({
    query: "remove that directory",
    isTerminal: true,
    stdout: mockStdout,
    stderr: mockStderr,
    stdinReader: mockStdinReader,
    translateFn: mockTranslate,
    execFn: mockExec,
    recordAuditFn: mockRecordAudit,
    readConfigFn: () => ({
      provider: "openai",
      apiKey: "test-key",
      model: "gpt-4o-mini",
      baseUrl: "https://api.openai.com/v1",
    }),
  });

  expect(code).toBe(0);
  expect(readLineCalls).toBe(0);
  expect(execCalls).toBe(0);

  // 🚨 banner + 独立原因行(danger reasonKey 渲染)+ 命令行 + 手动提示
  const allStdout = stdoutChunks.join("");
  expect(allStdout).toContain("🚨");
  expect(allStdout).toContain("Reason:");
  expect(allStdout).toContain("Recursive delete, possibly irreversible");
  expect(allStdout).toContain("→ rm -rf /tmp/x");
  expect(allStdout).toContain("Please run this command in your terminal");
  expect(allStdout).not.toContain("Proceed?");

  const rejectAudit = auditLogs.find((l) => l.tool === "copilot_reject");
  expect(rejectAudit).toBeDefined();
  expect(rejectAudit?.outcome).toBe("denied");
  expect(rejectAudit?.args.risk).toBe("danger");
  expect(rejectAudit?.args.reason).toBe("risk.pattern.rm_rf");

  // 展示路径在 translate 审计前分流:return 前无 copilot_confirm/copilot_cancel
  expect(auditLogs.some((l) => l.tool === "copilot_confirm" || l.tool === "copilot_cancel")).toBe(false);
});

Deno.test("Copilot Main - L0 translate audit carries risk_level='safe'; display path reject carries tiered risk", async () => {
  // risk_level 审计字段(pivot 决策 11):L0 的 copilot_translate 与
  // 两条 confirm 路径(auto / y-n)均携带 risk_level;展示路径的档位
  // 由 copilot_reject 的 risk 字段承载(前两个用例已钉)。
  setup();
  const mockTranslate = async () => {
    return JSON.stringify({
      command: "uptime",
      args: [],
      explanation: "Show uptime",
    });
  };

  const mockExec = async () => ({
    stdout: " 14:00 up 1 day\n",
    stderr: "",
    returncode: 0,
    error: null,
  });

  // 场景 A:非 TTY 白名单只读诊断直接运行
  const codeA = await runCopilot({
    query: "show uptime",
    isTerminal: false,
    yes: true,
    stdout: mockStdout,
    stderr: mockStderr,
    translateFn: mockTranslate,
    execFn: mockExec,
    recordAuditFn: mockRecordAudit,
    readConfigFn: () => ({
      provider: "openai",
      apiKey: "test-key",
      model: "gpt-4o-mini",
      baseUrl: "https://api.openai.com/v1",
    }),
  });

  expect(codeA).toBe(0);
  const translateA = auditLogs.find((l) => l.tool === "copilot_translate");
  expect(translateA).toBeDefined();
  expect(translateA?.args.risk_level).toBe("safe");
  expect(translateA?.args.round).toBe(0);
  const confirmA = auditLogs.find((l) => l.tool === "copilot_confirm");
  expect(confirmA?.args.risk_level).toBe("safe");
  expect(confirmA?.args.auto).toBe(true);

  setup();

  // 场景 B:TTY y/n 确认 "y" 后执行
  const mockStdinReader = async (_prompt?: string) => "y";
  const codeB = await runCopilot({
    query: "show uptime",
    isTerminal: true,
    stdout: mockStdout,
    stderr: mockStderr,
    stdinReader: mockStdinReader,
    translateFn: mockTranslate,
    execFn: mockExec,
    recordAuditFn: mockRecordAudit,
    readConfigFn: () => ({
      provider: "openai",
      apiKey: "test-key",
      model: "gpt-4o-mini",
      baseUrl: "https://api.openai.com/v1",
    }),
  });

  expect(codeB).toBe(0);
  const translateB = auditLogs.find((l) => l.tool === "copilot_translate");
  expect(translateB?.args.risk_level).toBe("safe");
  const confirmB = auditLogs.find((l) => l.tool === "copilot_confirm");
  expect(confirmB?.args.risk_level).toBe("safe");
  expect(confirmB?.args.auto).toBe(false);
});

Deno.test("Copilot Main - defaultReadStdinAll reads piped input via Deno.stdin.readable (Deno 2)", async () => {
  // 背景：Deno 2 移除了 Deno.readAll，管道读取必须走 stdin.readable 异步迭代。
  // 本测试临时替换 Deno.stdin.readable 为编码字节流，验证解码 + trim 行为。
  const denoGlobal = globalThis as any;
  const originalDescriptor = Object.getOwnPropertyDescriptor(denoGlobal.Deno, "stdin");
  const originalStdin = denoGlobal.Deno.stdin;
  const encoder = new TextEncoder();
  const restore = () => {
    if (originalDescriptor) {
      Object.defineProperty(denoGlobal.Deno, "stdin", originalDescriptor);
    }
  };
  try {
    // 模拟管道输入：带首尾空白与换行的中文查询
    const payload = "  查询内存 \n";
    const chunks = [payload.slice(0, 4), payload.slice(4)]; // 分块到达，验证流式累积
    denoGlobal.Deno.stdin = {
      readable: new ReadableStream<Uint8Array>({
        start(controller) {
          for (const c of chunks) {
            controller.enqueue(encoder.encode(c));
          }
          controller.close();
        },
      }),
    };

    const result = await defaultReadStdinAll();
    expect(result).toBe("查询内存");
  } finally {
    // 恢复原始 stdin，避免污染其他测试
    restore();
    void originalStdin;
  }
});

Deno.test("Copilot Main - defaultReadStdinAll returns empty string when stream errors", async () => {
  // 静默失败场景：流读取抛异常时应返回空串（调用方据此判断空查询）
  const denoGlobal = globalThis as any;
  const originalDescriptor = Object.getOwnPropertyDescriptor(denoGlobal.Deno, "stdin");
  try {
    denoGlobal.Deno.stdin = {
      readable: new ReadableStream<Uint8Array>({
        start(controller) {
          controller.error(new Error("simulated pipe failure"));
        },
      }),
    };

    const result = await defaultReadStdinAll();
    expect(result).toBe("");
  } finally {
    if (originalDescriptor) {
      Object.defineProperty(denoGlobal.Deno, "stdin", originalDescriptor);
    }
  }
});
