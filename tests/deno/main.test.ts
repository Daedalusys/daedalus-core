import { expect } from "jsr:@std/expect@1";
import { defaultReadStdinAll, defaultTxBegin, runCopilot, parseArgs, readStateSummary, formatStateSummary, VERSION } from "../../daedalus/plugin/copilot/main.ts";
import type { StateSummary, StateSummaryEntry } from "../../daedalus/plugin/copilot/main.ts";
import type { TxApplyOutcome, TxJournal, TxPreviewOutcome, TxProposeOutcome, TxStep } from "../../daedalus/plugin/copilot/exec.ts";
import { initI18n } from "../../daedalus/plugin/copilot/i18n.ts";

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

// state 记忆基线锁(T20 同族纪律):todo 28 后 runQueryTurn 默认经
// readStateSummary 真实读文件。把 DAEDALUS_STATE_PATH 钉到不存在的临时
// 路径 → 全文件默认态读恒 NotFound 静默(env 存在即唯一候选,镜像
// internal/dirs.File 的 env 独占链),开发者真实 ~/.local/share 状态
// 绝不泄漏进断言。readStateSummary 专项测试再各自覆写该 env。
if ((globalThis as any).Deno?.env?.set) {
  Deno.env.set("DAEDALUS_STATE_PATH", "/tmp/daedalus-main-test-baseline-missing.jsonl");
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

// ═══════════════════════════════════════════════════════════════════════════
// todo 27:事务通道接线(tx.propose / tx.apply / tx.rollback → execTx* 分派)
//
// LLM 事务提议编码:command = "tx.propose"|"tx.apply"|"tx.rollback",
// args = [目标, 期望态?]。safe 定级(tx_propose 恒 safe;tx_apply
// started|stopped)走 begin→propose→preview→y/n→apply;
// caution/danger(tx_apply restarted/enabled/disabled/reload、tx_rollback)
// 只展示,永不 apply。execTx* 三函数与 begin 全部 mock 注入(除
// defaultTxBegin 真实假二进制用例),沿用 setup/teardown 风格。
// ═══════════════════════════════════════════════════════════════════════════

const TX_ID = "a1b2c3d4e5f60718";

/** 构造通过 exec.ts asJournal 五键形态校验的事务日志夹具。 */
function txJournalFixture(status: string, steps: TxStep[]): TxJournal {
  return {
    id: TX_ID,
    created_at: "2026-09-07T00:00:00Z",
    status: status as TxJournal["status"],
    steps,
    rollback_plan: { steps: [] },
  };
}

const TX_STEP: TxStep = {
  index: 1,
  adapter: "service.set",
  args: { name: "nginx", desired_state: "started" },
  before_state: { ActiveState: "inactive" },
  after_state: { ActiveState: "active" },
  op_result: { returncode: 0 },
};

/** 记录调用顺序的 4 个 tx mock(成功形态;个别用例单独覆写)。 */
function makeTxMocks() {
  const calls: string[] = [];
  return {
    calls,
    txBeginFn: async () => {
      calls.push("begin");
      return { ok: true, txId: TX_ID } as const;
    },
    txProposeFn: async (_txId: string, _adapter: string, _args: unknown): Promise<TxProposeOutcome> => {
      calls.push("propose");
      const journal = txJournalFixture("proposed", [TX_STEP]);
      return { ok: true, journal, step: TX_STEP, beforeState: TX_STEP.before_state, afterState: TX_STEP.after_state };
    },
    txPreviewFn: async (_txId: string): Promise<TxPreviewOutcome> => {
      calls.push("preview");
      return {
        ok: true,
        journal: txJournalFixture("proposed", [TX_STEP]),
        diff: [{
          index: 1,
          adapter: "service.set",
          args: TX_STEP.args,
          beforeState: TX_STEP.before_state,
          afterState: TX_STEP.after_state,
        }],
      };
    },
    txApplyFn: async (_txId: string): Promise<TxApplyOutcome> => {
      calls.push("apply");
      return { ok: true, journal: txJournalFixture("applied", [TX_STEP]), opResults: [{ returncode: 0 }] };
    },
  };
}

function txTranslate(command: string, args: string[]) {
  return async () => JSON.stringify({
    command,
    args,
    explanation: "Apply desired_state to a user-scope service",
  });
}

const txConfig = () => ({
  provider: "openai" as const,
  apiKey: "test-key",
  model: "gpt-4o-mini",
  baseUrl: "https://api.openai.com/v1",
});

Deno.test("Copilot Main - tx L0 happy path: begin -> propose -> preview -> 'y' -> apply, no shell exec", async () => {
  setup();
  const tx = makeTxMocks();
  let shellExecCalls = 0;
  const inputs = ["y"];
  const mockStdinReader = async (_prompt?: string) => inputs.shift() ?? null;

  const code = await runCopilot({
    query: "start the nginx user service",
    isTerminal: true,
    stdout: mockStdout,
    stderr: mockStderr,
    stdinReader: mockStdinReader,
    translateFn: txTranslate("tx.apply", ["nginx", "started"]),
    execFn: async () => {
      shellExecCalls++;
      return { stdout: "", stderr: "", returncode: 0, error: null };
    },
    recordAuditFn: mockRecordAudit,
    readConfigFn: txConfig,
    txBeginFn: tx.txBeginFn,
    txProposeFn: tx.txProposeFn,
    txPreviewFn: tx.txPreviewFn,
    txApplyFn: tx.txApplyFn,
  });

  expect(code).toBe(0);
  // 严格调用序:begin 先行(execTxPropose 不自动 begin),propose/preview 均先于 y,apply 最后
  expect(tx.calls).toEqual(["begin", "propose", "preview", "apply"]);
  expect(shellExecCalls).toBe(0); // 事务通道绝不走 execAllowlisted

  const allStdout = stdoutChunks.join("");
  // preview 的「计划前 → 计划后」diff 在确认提示之前展示
  expect(allStdout).toContain(`Planned changes (transaction ${TX_ID}`);
  expect(allStdout).toContain("step 1 [service.set]");
  expect(allStdout).toContain('{"ActiveState":"inactive"} -> {"ActiveState":"active"}');
  expect(allStdout).toContain(`Transaction ${TX_ID} applied`);

  // 审计链:translate(带 tx_kind)→ tx_propose → tx_apply(auto:false = y 显式授权)
  const translateLog = auditLogs.find((l) => l.tool === "copilot_translate");
  expect(translateLog?.args.risk_level).toBe("safe");
  expect(translateLog?.args.tx_kind).toBe("tx_apply");
  const proposeLog = auditLogs.find((l) => l.tool === "copilot_tx_propose");
  expect(proposeLog?.outcome).toBe("success");
  expect(proposeLog?.args.tx_id).toBe(TX_ID);
  expect(proposeLog?.args.adapter).toBe("service.set");
  expect(proposeLog?.args.target).toBe("nginx");
  expect(proposeLog?.args.desired_state).toBe("started");
  const applyLog = auditLogs.find((l) => l.tool === "copilot_tx_apply");
  expect(applyLog?.outcome).toBe("success");
  expect(applyLog?.args.auto).toBe(false);
  // shell 通道的 copilot_confirm 绝不出现在事务轮次
  expect(auditLogs.some((l) => l.tool === "copilot_confirm")).toBe(false);
});

Deno.test("Copilot Main - tx L0 user says 'n': proposal dropped, apply never called, copilot_tx_reject with 'user denied tx'", async () => {
  setup();
  const tx = makeTxMocks();
  const inputs = ["n"];
  const mockStdinReader = async (_prompt?: string) => inputs.shift() ?? null;

  const code = await runCopilot({
    query: "start nginx",
    isTerminal: true,
    stdout: mockStdout,
    stderr: mockStderr,
    stdinReader: mockStdinReader,
    translateFn: txTranslate("tx.apply", ["nginx", "started"]),
    execFn: async () => ({ stdout: "", stderr: "", returncode: 0, error: null }),
    recordAuditFn: mockRecordAudit,
    readConfigFn: txConfig,
    txBeginFn: tx.txBeginFn,
    txProposeFn: tx.txProposeFn,
    txPreviewFn: tx.txPreviewFn,
    txApplyFn: tx.txApplyFn,
  });

  expect(code).toBe(0);
  // begin/propose/preview 已发生(仅快照,无副作用);apply 绝不调用
  expect(tx.calls).toEqual(["begin", "propose", "preview"]);
  const rejectLog = auditLogs.find((l) => l.tool === "copilot_tx_reject");
  expect(rejectLog?.outcome).toBe("denied");
  expect(rejectLog?.args.reason).toBe("user denied tx");
  expect(rejectLog?.args.tx_id).toBe(TX_ID);
  expect(auditLogs.some((l) => l.tool === "copilot_tx_apply")).toBe(false);
  expect(stdoutChunks.join("")).toContain("Transaction dropped");
});

Deno.test("Copilot Main - tx L1 (restarted -> caution) display-only: no begin/propose/apply, no prompt", async () => {
  setup();
  const tx = makeTxMocks();
  let readLineCalls = 0;

  const code = await runCopilot({
    query: "restart nginx",
    isTerminal: true,
    stdout: mockStdout,
    stderr: mockStderr,
    stdinReader: async () => {
      readLineCalls++;
      return "y";
    },
    translateFn: txTranslate("tx.apply", ["nginx", "restarted"]),
    execFn: async () => ({ stdout: "", stderr: "", returncode: 0, error: null }),
    recordAuditFn: mockRecordAudit,
    readConfigFn: txConfig,
    txBeginFn: tx.txBeginFn,
    txProposeFn: tx.txProposeFn,
    txPreviewFn: tx.txPreviewFn,
    txApplyFn: tx.txApplyFn,
  });

  expect(code).toBe(0);
  expect(readLineCalls).toBe(0);
  expect(tx.calls).toEqual([]); // L1 绝不进事务执行通道(连 begin 都不发起)

  const allStdout = stdoutChunks.join("");
  expect(allStdout).toContain("⚠");
  expect(allStdout).toContain("→ tx.apply nginx restarted");
  expect(allStdout).toContain("Please run this command in your terminal");

  const rejectLog = auditLogs.find((l) => l.tool === "copilot_reject");
  expect(rejectLog?.outcome).toBe("denied");
  expect(rejectLog?.args.risk).toBe("caution");
  expect(rejectLog?.args.tx_kind).toBe("tx_apply");
  expect(rejectLog?.args.target).toBe("nginx");
  expect(rejectLog?.args.desired_state).toBe("restarted");
});

Deno.test("Copilot Main - tx L2 (reload -> danger) display-only: danger banner + reason, apply never reachable", async () => {
  setup();
  const tx = makeTxMocks();

  const code = await runCopilot({
    query: "reload nginx",
    isTerminal: true,
    stdout: mockStdout,
    stderr: mockStderr,
    stdinReader: async () => "y",
    translateFn: txTranslate("tx.apply", ["nginx", "reload"]),
    execFn: async () => ({ stdout: "", stderr: "", returncode: 0, error: null }),
    recordAuditFn: mockRecordAudit,
    readConfigFn: txConfig,
    txBeginFn: tx.txBeginFn,
    txProposeFn: tx.txProposeFn,
    txPreviewFn: tx.txPreviewFn,
    txApplyFn: tx.txApplyFn,
  });

  expect(code).toBe(0);
  expect(tx.calls).toEqual([]);

  const allStdout = stdoutChunks.join("");
  expect(allStdout).toContain("🚨");
  expect(allStdout).toContain("Reason:");
  // danger 借 risk.pattern.shutdown 文案(T24 钉:reload 中断在途服务)
  expect(allStdout).toContain("Shutdown/reboot; interrupts all sessions");

  const rejectLog = auditLogs.find((l) => l.tool === "copilot_reject");
  expect(rejectLog?.args.risk).toBe("danger");
  expect(rejectLog?.args.reason).toBe("risk.pattern.shutdown");
  expect(auditLogs.some((l) => l.tool === "copilot_tx_apply" || l.tool === "copilot_tx_propose")).toBe(false);
});

Deno.test("Copilot Main - tx_rollback (caution) for an unknown tx-id is display-only, no CLI spawn, structured hint, no stack", async () => {
  setup();
  const tx = makeTxMocks();

  const code = await runCopilot({
    query: "rollback transaction deadbeefdeadbeef01",
    isTerminal: true,
    stdout: mockStdout,
    stderr: mockStderr,
    stdinReader: async () => "y",
    translateFn: txTranslate("tx.rollback", ["deadbeefdeadbeef01"]),
    execFn: async () => ({ stdout: "", stderr: "", returncode: 0, error: null }),
    recordAuditFn: mockRecordAudit,
    readConfigFn: txConfig,
    txBeginFn: tx.txBeginFn,
    txProposeFn: tx.txProposeFn,
    txPreviewFn: tx.txPreviewFn,
    txApplyFn: tx.txApplyFn,
  });

  // plan Failure QA 的当前形态:rollback 定级 caution → 展示路径,
  // copilot 连 daedalus-tx 都不 spawn(「不存在的事务」错误在 v1 通道不可达),
  // 用户拿到的是手动执行提示而非栈。
  expect(code).toBe(0);
  expect(tx.calls).toEqual([]);
  const allOutput = stdoutChunks.join("") + stderrChunks.join("");
  expect(allOutput).not.toContain("    at ");
  const rejectLog = auditLogs.find((l) => l.tool === "copilot_reject");
  expect(rejectLog?.args.tx_kind).toBe("tx_rollback");
  expect(rejectLog?.args.target).toBe("deadbeefdeadbeef01");
});

Deno.test("Copilot Main - tx proposal with empty target fails closed before begin (zero spawn, exit 126)", async () => {
  setup();
  const tx = makeTxMocks();

  const code = await runCopilot({
    query: "apply something",
    isTerminal: true,
    stdout: mockStdout,
    stderr: mockStderr,
    stdinReader: async () => "y",
    // args 缺目标:classifyTxProposal 抛 "target is empty"(plan QA 字面量)
    translateFn: txTranslate("tx.apply", []),
    execFn: async () => ({ stdout: "", stderr: "", returncode: 0, error: null }),
    recordAuditFn: mockRecordAudit,
    readConfigFn: txConfig,
    txBeginFn: tx.txBeginFn,
    txProposeFn: tx.txProposeFn,
    txPreviewFn: tx.txPreviewFn,
    txApplyFn: tx.txApplyFn,
  });

  expect(code).toBe(126);
  expect(tx.calls).toEqual([]); // 分类先行于开账:begin 绝不被调用
  expect(stderrChunks.join("")).toContain("classifyTxProposal: target is empty");
  const rejectLog = auditLogs.find((l) => l.tool === "copilot_reject");
  expect(rejectLog?.outcome).toBe("denied");
});

Deno.test("Copilot Main - tx apply structured failure renders i18n error (no stack trace), audits copilot_error, exits 1", async () => {
  setup();
  const tx = makeTxMocks();
  tx.txApplyFn = async (_txId: string): Promise<TxApplyOutcome> => {
    tx.calls.push("apply");
    return {
      ok: false,
      kind: "tx_runtime_error",
      returncode: 1,
      error: "step 1 failed: systemctl exit 1",
      stderr: "",
    };
  };

  const code = await runCopilot({
    query: "start nginx",
    isTerminal: true,
    stdout: mockStdout,
    stderr: mockStderr,
    stdinReader: async () => "y",
    translateFn: txTranslate("tx.apply", ["nginx", "started"]),
    execFn: async () => ({ stdout: "", stderr: "", returncode: 0, error: null }),
    recordAuditFn: mockRecordAudit,
    readConfigFn: txConfig,
    txBeginFn: tx.txBeginFn,
    txProposeFn: tx.txProposeFn,
    txPreviewFn: tx.txPreviewFn,
    txApplyFn: tx.txApplyFn,
  });

  expect(code).toBe(1);
  expect(tx.calls).toEqual(["begin", "propose", "preview", "apply"]);
  const stderr = stderrChunks.join("");
  // T26 TxFailure 的 error 字段经 t("tx.error.run") 渲染;绝不抛栈
  expect(stderr).toContain("Transaction step apply failed: step 1 failed: systemctl exit 1");
  expect(stderr).not.toContain("    at ");
  // 成功文案「Transaction <id> applied」绝不得出现(preview header 的
  // "applied only after your confirmation" 是子串陷阱,断言必用完整句)
  expect(stdoutChunks.join("")).not.toContain(`Transaction ${TX_ID} applied`);
  const errLog = auditLogs.find((l) => l.tool === "copilot_error");
  expect(errLog?.args.stage).toBe("apply");
  expect(errLog?.args.tx_id).toBe(TX_ID);
});

Deno.test("Copilot Main - defaultTxBegin spawns DAEDALUS_TX_BIN fake binary (real process) and parses tx_id per stdout contract", async () => {
  // 真实假二进制(T26 TX_FIXTURE 同族思路,begin 单行 JSON 文档即可):
  // 验证 begin 解析链的进程边界保真度 —— argv 直传 + 单层转义 JSON 一次解析。
  const dir = await Deno.makeTempDir({ prefix: "t27-txbegin-" });
  const okBin = `${dir}/tx-ok`;
  await Deno.writeTextFile(okBin, `#!/bin/sh\necho '{"tx_id":"${TX_ID}"}'\n`);
  await Deno.chmod(okBin, 0o755);
  const badBin = `${dir}/tx-bad`;
  await Deno.writeTextFile(badBin, `#!/bin/sh\necho '{"error":"no tx dir available"}'\nexit 1\n`);
  await Deno.chmod(badBin, 0o755);

  try {
    Deno.env.set("DAEDALUS_TX_BIN", okBin);
    const ok = await defaultTxBegin();
    expect(ok.ok).toBe(true);
    if (ok.ok) expect(ok.txId).toBe(TX_ID);

    Deno.env.set("DAEDALUS_TX_BIN", badBin);
    const bad = await defaultTxBegin();
    expect(bad.ok).toBe(false);
    if (!bad.ok) {
      // 失败契约:非零退出也吐 {"error":...},优先取文档文本(永不 throw)
      expect(bad.error).toContain("no tx dir available");
    }
  } finally {
    Deno.env.delete("DAEDALUS_TX_BIN");
    await Deno.remove(dir, { recursive: true });
  }
});

// ─────────────────────────────────────────────────────────────────────────────
// todo 28:state 记忆注入 runQueryTurn 测试组
//
// 覆盖三面:① 非空摘要 → translate 的 system prompt 追加段 + revise 循环
// history[0] 同构注入;② 读取失败(PermissionDenied 分类)→ stderr 可见、
// 无审计、翻译照常、绝不 throw;③ 空/缺失态 → 兜底文案绝不污染 prompt。
// 另有 readStateSummary 直调测试钉真实文件解析链(newest-wins / 坏行 /
// kind 过滤 / NotFound 静默 / PermissionDenied 可见)。
// ─────────────────────────────────────────────────────────────────────────────

const stateConfig = () => ({
  provider: "openai" as const,
  apiKey: "test-key",
  model: "gpt-4o-mini",
  baseUrl: "https://api.openai.com/v1",
});

function makeStateEntry(
  name: string,
  activeState: string,
  observedAt: string,
  extra: Record<string, string> = {},
): StateSummaryEntry {
  return {
    name,
    observedAt,
    desiredState: "",
    properties: { ActiveState: activeState, ...extra },
  };
}

function stateSummaryOf(entries: StateSummaryEntry[], errors: string[] = []): StateSummary {
  return { entries, text: formatStateSummary(entries), errors };
}

Deno.test("Copilot Main - TestRunQueryTurn_StateInjection: state summary appended to system prompt before translate", async () => {
  await initI18n();
  setup();
  let capturedSystemContext: string | undefined;
  let capturedQuery = "";

  const entries = [
    makeStateEntry("sshd.service", "active", "2026-09-07T02:00:00Z", {
      SubState: "running",
      LoadState: "loaded", // 不在 curated 三项子集,绝不得出现在摘要行
    }),
    makeStateEntry("crond.service", "inactive", "2026-09-07T02:10:00Z"),
  ];

  const code = await runCopilot({
    query: "show disk usage",
    isTerminal: true,
    dryRun: true,
    stdout: mockStdout,
    stderr: mockStderr,
    translateFn: async (_q: string, _o?: unknown, systemContext?: string) => {
      capturedQuery = _q;
      capturedSystemContext = systemContext;
      return JSON.stringify({ command: "df", args: ["-h"], explanation: "Show disk usage" });
    },
    recordAuditFn: mockRecordAudit,
    readConfigFn: stateConfig,
    readStateFn: async () => stateSummaryOf(entries),
  });

  expect(code).toBe(0);
  // 用户查询原文绝不掺入状态数据(注入只走 system prompt 追加段)
  expect(capturedQuery).toBe("show disk usage");
  // 注入内容 = formatStateSummary 产物,逐字节一致
  expect(capturedSystemContext).toBe(formatStateSummary(entries));
  const block = capturedSystemContext ?? "";
  // 块标记(与 T30 落位的 en_US state.summary.header 文案一致,大小写不敏感钉 token)
  expect(block.toLowerCase()).toContain("known state");
  expect(block).toContain("sshd.service: ActiveState=active, SubState=running (observed 2026-09-07T02:00:00Z)");
  expect(block).toContain("crond.service: ActiveState=inactive");
  expect(block).not.toContain("LoadState");
  // 兜底文案绝不混入非空摘要
  expect(block).not.toContain("No service state recorded yet.");
  // dry-run 正常收尾 + 状态读取零审计(plan 条款:NotFound/PermissionDenied 均不落审计事件)
  expect(stdoutChunks.join("")).toContain("[dry-run] Proposed command");
  expect(auditLogs.some((l) => l.tool.includes("state"))).toBe(false);
  expect(stderrChunks.join("")).toBe("");
});

Deno.test("Copilot Main - TestRunQueryTurn_StateInjection_ReviseHistory: revise loop system message carries the same state block", async () => {
  await initI18n();
  setup();
  let capturedHistory: Array<{ role: string; content: string }> = [];

  const entries = [makeStateEntry("nginx.service", "active", "2026-09-07T03:00:00Z")];
  const block = formatStateSummary(entries);

  // 交互流:df -h 提议 → [n] 反馈 → revise(捕获 history)→ 新提议 free -m → [y] 执行
  const inputs = ["n", "use mem info instead", "y"];
  const code = await runCopilot({
    query: "check memory",
    isTerminal: true,
    interactive: true,
    stdout: mockStdout,
    stderr: mockStderr,
    stdinReader: async () => inputs.shift() ?? null,
    translateFn: async () => JSON.stringify({ command: "df", args: ["-h"], explanation: "Show disk usage" }),
    reviseFn: async (history: Array<{ role: "system" | "user" | "assistant"; content: string }>) => {
      capturedHistory = history;
      return JSON.stringify({ command: "free", args: ["-m"], explanation: "Show memory" });
    },
    execFn: async () => ({ stdout: "ok\n", stderr: "", returncode: 0, error: null }),
    recordAuditFn: mockRecordAudit,
    readConfigFn: stateConfig,
    readStateFn: async () => stateSummaryOf(entries),
  });

  expect(code).toBe(0);
  // 修订轮 system 消息 = 基础提示词 + 与首轮 translate 同构的 KNOWN STATE 追加段
  expect(capturedHistory.length).toBeGreaterThanOrEqual(1);
  expect(capturedHistory[0].role).toBe("system");
  expect(capturedHistory[0].content).toContain(block);
  expect(capturedHistory[0].content).toContain("nginx.service: ActiveState=active");
});

Deno.test("Copilot Main - TestRunQueryTurn_StateReadFailure: permission-denied paths print stderr + continue, prompt unpolluted, zero audit", async () => {
  await initI18n();
  setup();
  let capturedSystemContext: string | undefined = "sentinel-not-called";

  const code = await runCopilot({
    query: "show uptime",
    isTerminal: true,
    dryRun: true,
    stdout: mockStdout,
    stderr: mockStderr,
    translateFn: async (_q: string, _o?: unknown, systemContext?: string) => {
      capturedSystemContext = systemContext;
      return JSON.stringify({ command: "uptime", args: [], explanation: "Show uptime" });
    },
    recordAuditFn: mockRecordAudit,
    readConfigFn: stateConfig,
    // 模拟 readStateSummary 的真实降级形状:entries 空 + 兜底 text + errors 路径清单
    readStateFn: async () => ({
      entries: [],
      text: "No service state recorded yet.",
      errors: ["/var/lib/daedalus/state.jsonl"],
    }),
  });

  // 优雅降级:退出码照常 0,LLM 翻译照常进行
  expect(code).toBe(0);
  // 配置期可见性:逐字钉 plan 文案(en_US state.summary.error)
  expect(stderrChunks.join("")).toContain(
    "copilot: state read permission denied: /var/lib/daedalus/state.jsonl",
  );
  // 失败态绝不把兜底文案注入 prompt
  expect(capturedSystemContext).toBeUndefined();
  // 翻译事件照常落审计;状态读取本身零审计条目
  expect(auditLogs.some((l) => l.tool === "copilot_translate")).toBe(true);
  expect(auditLogs.some((l) => l.tool.includes("state"))).toBe(false);
});

Deno.test("Copilot Main - TestRunQueryTurn_EmptyState: no entries means no prompt pollution (fallback text never injected)", async () => {
  await initI18n();
  setup();
  let capturedSystemContext: string | undefined = "sentinel-not-called";

  const code = await runCopilot({
    query: "list files",
    isTerminal: true,
    dryRun: true,
    stdout: mockStdout,
    stderr: mockStderr,
    translateFn: async (_q: string, _o?: unknown, systemContext?: string) => {
      capturedSystemContext = systemContext;
      return JSON.stringify({ command: "ls", args: [], explanation: "List files" });
    },
    recordAuditFn: mockRecordAudit,
    readConfigFn: stateConfig,
    // 空缓存正常态:text 是兜底文案,但 entries 长度为 0 → 不注入
    readStateFn: async () => ({
      entries: [],
      text: "No service state recorded yet.",
      errors: [],
    }),
  });

  expect(code).toBe(0);
  expect(capturedSystemContext).toBeUndefined();
  expect(stderrChunks.join("")).toBe("");
  expect(stdoutChunks.join("")).toContain("[dry-run] Proposed command");
});

// readStateSummary 直调测试:钉真实文件读取链(dirs 候选解析 + JSONL 容错
// 解析 + newest-wins + kind 过滤 + 错误分类)。用后即删,不触碰真实
// /var/lib/daedalus 与 $HOME 状态记忆(baseline env 已在文件顶锁定)。
async function withStateEnv(path: string, fn: () => Promise<void>): Promise<void> {
  const prev = Deno.env.get("DAEDALUS_STATE_PATH");
  Deno.env.set("DAEDALUS_STATE_PATH", path);
  try {
    await fn();
  } finally {
    if (prev === undefined) Deno.env.delete("DAEDALUS_STATE_PATH");
    else Deno.env.set("DAEDALUS_STATE_PATH", prev);
  }
}

Deno.test("readStateSummary - parses real state.jsonl: service-only, newest-wins, bad lines skipped, deterministic sort", async () => {
  await initI18n();
  const dir = await Deno.makeTempDir({ prefix: "t28-state-" });
  const file = `${dir}/state.jsonl`;
  const lines = [
    // sshd 旧观测(将被同文件后写的 sshd 新观测覆盖)
    JSON.stringify({ kind: "service", name: "sshd.service", observed_at: "2026-09-06T10:00:00Z", payload: { kind: "service", name: "sshd.service", desired_state: "", properties: { ActiveState: "inactive", SubState: "dead" } } }),
    // 坏行:非法 JSON(Go Read 容错姿态的 TS 镜像,静默跳过)
    "{ broken json",
    // 非 service 条目:kind=package 过滤
    JSON.stringify({ kind: "package", name: "bash", observed_at: "2026-09-06T11:00:00Z", payload: { kind: "package", name: "bash", desired_state: "", properties: {} } }),
    // nginx 单条
    JSON.stringify({ kind: "service", name: "nginx.service", observed_at: "2026-09-06T12:00:00Z", payload: { kind: "service", name: "nginx.service", desired_state: "", properties: { ActiveState: "active", SubState: "running", UnitFileState: "enabled" } } }),
    // 非对象行(数组)→ 静默跳过
    "[1,2,3]",
    // sshd 新观测:后写即新
    JSON.stringify({ kind: "service", name: "sshd.service", observed_at: "2026-09-07T02:00:00Z", payload: { kind: "service", name: "sshd.service", desired_state: "", properties: { ActiveState: "active", SubState: "running" } } }),
  ];
  await Deno.writeTextFile(file, lines.join("\n") + "\n");

  await withStateEnv(file, async () => {
    const summary = await readStateSummary();
    expect(summary.errors).toEqual([]);
    // kind 过滤 + newest-wins:恰 sshd + nginx 两条,按 name 升序
    expect(summary.entries.map((e) => e.name)).toEqual(["nginx.service", "sshd.service"]);
    const sshd = summary.entries[1];
    expect(sshd.observedAt).toBe("2026-09-07T02:00:00Z"); // 后写行胜出
    expect(sshd.properties.ActiveState).toBe("active");
    expect(sshd.properties.SubState).toBe("running");
    const nginx = summary.entries[0];
    expect(nginx.properties.UnitFileState).toBe("enabled");
    // text = 追加块:header + 两行,坏行/非 service 绝不泄漏
    expect(summary.text).toContain("nginx.service: ActiveState=active, SubState=running, UnitFileState=enabled");
    expect(summary.text).toContain("sshd.service: ActiveState=active, SubState=running (observed 2026-09-07T02:00:00Z)");
    expect(summary.text).not.toContain("bash");
    expect(summary.text).not.toContain("2026-09-06T10:00:00Z"); // 旧观测不重现
  });

  await Deno.remove(dir, { recursive: true });
});

Deno.test("readStateSummary - missing file degrades silently (NotFound = v1 normal state, no error entries)", async () => {
  await initI18n();
  const dir = await Deno.makeTempDir({ prefix: "t28-missing-" });
  await withStateEnv(`${dir}/no-such-state.jsonl`, async () => {
    const summary = await readStateSummary();
    expect(summary.entries).toEqual([]);
    expect(summary.errors).toEqual([]); // NotFound 静默,不记 errors
    // 兜底文案在 text 上(由 formatStateSummary 的 state.summary.empty 渲染),
    // 注入与否由调用方按 entries.length 裁决(runQueryTurn 已测:绝不注入)
    expect(summary.text).toBe("No service state recorded yet.");
  });
  await Deno.remove(dir, { recursive: true });
});

Deno.test("readStateSummary - PermissionDenied is classified into errors (never throws, developer-visible)", async () => {
  await initI18n();
  // root 下 DAC 恒放行(EACCES 不可复现),与 Go 侧 dirs/state 测试同法 skip
  if (typeof (Deno as any).euid === "function" && (Deno as any).euid() === 0) {
    return;
  }
  const dir = await Deno.makeTempDir({ prefix: "t28-denied-" });
  const file = `${dir}/state.jsonl`;
  await Deno.writeTextFile(file, "whatever\n");
  await Deno.chmod(file, 0o000);

  await withStateEnv(file, async () => {
    let summary: StateSummary | null = null;
    let threw = false;
    try {
      summary = await readStateSummary();
    } catch {
      threw = true;
    }
    await Deno.chmod(file, 0o644); // 先恢复权限再断言/清理,永不 throw 是契约
    expect(threw).toBe(false);
    expect(summary).not.toBeNull();
    expect(summary!.errors).toEqual([file]); // 唯一候选被拒 → 记路径继续
    expect(summary!.entries).toEqual([]);
    expect(summary!.text).toBe("No service state recorded yet.");
  });

  await Deno.remove(dir, { recursive: true });
});
