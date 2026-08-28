import { expect } from "jsr:@std/expect@1";
import { defaultReadStdinAll, runCopilot, parseArgs, VERSION } from "../../daedalus/plugin/copilot/main.ts";

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

  // 新语义：-v 为 verbose，版本号改用 -V / --version
  const parsed4 = parseArgs(["-V"]);
  expect(parsed4.version).toBe(true);
  expect(parsed4.verbose).toBe(false);

  const parsed5 = parseArgs(["-v", "-i", "--dry-run", "list", "files"]);
  expect(parsed5.verbose).toBe(true);
  expect(parsed5.interactive).toBe(true);
  expect(parsed5.dryRun).toBe(true);
  expect(parsed5.version).toBe(false);
  expect(parsed5.query).toBe("list files");

  // --yes 保留为向后兼容别名（默认行为已是自动执行）
  const parsed6 = parseArgs(["--yes"]);
  expect(parsed6.yes).toBe(true);
  expect(parsed6.interactive).toBe(false);
  expect(parsed6.verbose).toBe(false);
  expect(parsed6.dryRun).toBe(false);

  const parsed7 = parseArgs(["--verbose", "--interactive"]);
  expect(parsed7.verbose).toBe(true);
  expect(parsed7.interactive).toBe(true);
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

Deno.test("Copilot Main - rejects non-allowlisted command from LLM with exit code 126 and denied audit", async () => {
  setup();
  let execCalled = false;

  const mockTranslate = async () => {
    return JSON.stringify({
      command: "rm",
      args: ["-rf", "/tmp/files"],
      explanation: "Delete files",
    });
  };

  const mockExec = async () => {
    execCalled = true;
    return { stdout: "", stderr: "", returncode: 0, error: null };
  };

  const code = await runCopilot({
    query: "delete temporary files",
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

  expect(code).toBe(126);
  expect(execCalled).toBe(false);
  expect(stderrChunks.join("")).toContain("Security policy rejection:");
  expect(stderrChunks.join("")).toContain("not in ALLOW_COMMANDS");

  // 验证审计记录
  const rejectAudit = auditLogs.find((l) => l.tool === "copilot_reject");
  expect(rejectAudit).toBeDefined();
  expect(rejectAudit?.outcome).toBe("denied");
  expect(rejectAudit?.args.query).toBe("delete temporary files");
  expect(rejectAudit?.args.reason).toContain("not in ALLOW_COMMANDS");
});

Deno.test("Copilot Main - rejects blocked paths from LLM with exit code 126", async () => {
  setup();
  let execCalled = false;

  const mockTranslate = async () => {
    return JSON.stringify({
      command: "cat",
      args: ["/etc/shadow"],
      explanation: "Read shadow file",
    });
  };

  const mockExec = async () => {
    execCalled = true;
    return { stdout: "", stderr: "", returncode: 0, error: null };
  };

  const code = await runCopilot({
    query: "read password hashes",
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

  expect(code).toBe(126);
  expect(execCalled).toBe(false);
  expect(stderrChunks.join("")).toContain("Security policy rejection:");
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

Deno.test("Copilot Main - handles LLM translation network error with exit code 1 and copilot_error audit", async () => {
  setup();
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
  expect(auditLogs.some((l) => l.tool === "copilot_error" && l.outcome === "error")).toBe(true);
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

// 新语义：非 TTY 管道环境默认直接自动执行，不再强制要求 --yes
Deno.test("Copilot Main - auto-executes in non-TTY environments without requiring --yes", async () => {
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
    "Interactive confirmation requested (-i) but stdin is not a TTY. Drop -i to auto-execute.",
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
  expect(stdoutChunks.join("")).toContain("[dry-run] Would execute: df -h");
  expect(
    auditLogs.some(
      (l) => l.tool === "copilot_confirm" && l.outcome === "success" && l.args.mode === "dry-run",
    ),
  ).toBe(true);
});

// 新语义：默认（TTY 无标志）立即自动执行，绝不读取 stdin 提示确认
Deno.test("Copilot Main - auto-executes immediately in TTY by default without ever reading stdin", async () => {
  setup();
  let readLineCalls = 0;
  let executedCommand = "";

  const mockStdinReader = async (_prompt?: string) => {
    readLineCalls++;
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
  expect(readLineCalls).toBe(0); // 默认路径从不弹出确认提示
  expect(executedCommand).toBe("df");
  expect(stdoutChunks.join("")).toContain("fs-table");
  expect(stdoutChunks.join("")).not.toContain("Proceed?");
  expect(
    auditLogs.some(
      (l) => l.tool === "copilot_confirm" && l.outcome === "success" && l.args.auto === true,
    ),
  ).toBe(true);
});

// 新语义：-v/--verbose 在执行前打印翻译结果，仍然自动执行
Deno.test("Copilot Main - verbose prints translated command and explanation then auto-executes", async () => {
  setup();
  let readLineCalls = 0;
  let executedCommand = "";

  const mockStdinReader = async (_prompt?: string) => {
    readLineCalls++;
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
    args: ["-v", "check disk"],
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
  expect(readLineCalls).toBe(0); // verbose 不弹交互确认
  const allStdout = stdoutChunks.join("");
  expect(allStdout).toContain("→ df -h");
  expect(allStdout).toContain("Show disk usage");
  expect(allStdout).toContain("executed");
  // verbose 输出必须出现在执行结果之前
  expect(allStdout.indexOf("→ df -h")).toBeLessThan(allStdout.indexOf("executed"));
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
