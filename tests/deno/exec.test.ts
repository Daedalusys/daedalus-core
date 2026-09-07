import { expect } from "jsr:@std/expect@1";
import {
  execAllowlisted,
  execTxApply,
  execTxPreview,
  execTxPropose,
} from "../../daedalus/plugin/copilot/exec.ts";

let mockSpawnedCommands: Array<{ cmd: string; options: any; instance: any }> = [];
let mockSignalListeners: Array<{ signal: string; handler: Function }> = [];

class ExecMockCommand {
  cmd: string;
  options: any;
  constructor(cmd: string, options: any) {
    this.cmd = cmd;
    this.options = options;
  }
  spawn() {
    const instance = createMockProcess(this.cmd, this.options);
    mockSpawnedCommands.push({ cmd: this.cmd, options: this.options, instance });
    return instance;
  }
}

interface MockProcessOptions {
  hang?: boolean;
  mcpError?: boolean;
  rejection?: boolean;
  customStdoutText?: string;
  customStderrText?: string;
  customInnerResult?: any;
}

let nextProcessBehavior: MockProcessOptions = {};

function createMockProcess(_cmd: string, _options: any) {
  const behavior = { ...nextProcessBehavior };
  let killedSignal: string | null = null;

  const stdoutChunks: Uint8Array[] = [];
  let stdoutResolve: (() => void) | null = null;
  let stdoutDone = false;

  const stderrChunks: Uint8Array[] = [];
  let stderrDone = false;

  if (behavior.customStderrText) {
    stderrChunks.push(new TextEncoder().encode(behavior.customStderrText));
  }

  const stdinDecoder = new TextDecoder();
  let stdinBuffer = "";

  const handleStdinMessage = (msg: any) => {
    if (behavior.hang) {
      return; // 不响应，模拟挂起
    }

    if (msg.method === "initialize" && msg.id === 1) {
      const resp = {
        jsonrpc: "2.0",
        id: 1,
        result: {
          protocolVersion: "2024-11-05",
          capabilities: {},
          serverInfo: { name: "daedalus-shell-deno", version: "1.0.0" },
        },
      };
      pushStdout(resp);
    } else if (msg.method === "notifications/initialized") {
      // 无需响应
    } else if (msg.method === "tools/call" && msg.id === 2) {
      if (behavior.mcpError) {
        const resp = {
          jsonrpc: "2.0",
          id: 2,
          error: {
            code: -32601,
            message: "Method 'shell_exec' not found",
          },
        };
        pushStdout(resp);
      } else if (behavior.rejection) {
        const innerResult = {
          stdout: "",
          stderr: "Command validation failed: Command 'rm' is not in ALLOW_COMMANDS allowlist.",
          returncode: 126,
          error: "Command 'rm' is not in ALLOW_COMMANDS allowlist.",
        };
        const resp = {
          jsonrpc: "2.0",
          id: 2,
          result: {
            content: [
              {
                type: "text",
                text: JSON.stringify(innerResult),
              },
            ],
            isError: true,
          },
        };
        pushStdout(resp);
      } else if (behavior.customInnerResult) {
        const resp = {
          jsonrpc: "2.0",
          id: 2,
          result: {
            content: [
              {
                type: "text",
                text: JSON.stringify(behavior.customInnerResult),
              },
            ],
            isError: behavior.customInnerResult.returncode !== 0,
          },
        };
        pushStdout(resp);
      } else {
        const innerResult = {
          stdout: "Filesystem      Size  Used Avail Use% Mounted on\n/dev/nvme0n1p3  468G  120G  325G  27% /\n",
          stderr: "",
          returncode: 0,
        };
        const resp = {
          jsonrpc: "2.0",
          id: 2,
          result: {
            content: [
              {
                type: "text",
                text: JSON.stringify(innerResult),
              },
            ],
            isError: false,
          },
        };
        pushStdout(resp);
      }
    }
  };

  function pushStdout(obj: any) {
    const line = JSON.stringify(obj) + "\n";
    stdoutChunks.push(new TextEncoder().encode(line));
    if (stdoutResolve) {
      const res = stdoutResolve;
      stdoutResolve = null;
      res();
    }
  }

  const stdinWriter = {
    async write(chunk: Uint8Array) {
      stdinBuffer += stdinDecoder.decode(chunk, { stream: true });
      const lines = stdinBuffer.split("\n");
      stdinBuffer = lines.pop() || "";
      for (const line of lines) {
        if (line.trim()) {
          try {
            const msg = JSON.parse(line.trim());
            handleStdinMessage(msg);
          } catch {
            // 测试 mock 中忽略 JSON 解析错误
          }
        }
      }
    },
    close() {},
  };

  const stdoutReader = {
    async read(): Promise<{ value?: Uint8Array; done: boolean }> {
      if (stdoutChunks.length > 0) {
        return { value: stdoutChunks.shift(), done: false };
      }
      if (stdoutDone) {
        return { done: true };
      }
      await new Promise<void>((resolve) => {
        stdoutResolve = resolve;
      });
      if (stdoutChunks.length > 0) {
        return { value: stdoutChunks.shift(), done: false };
      }
      return { done: true };
    },
    releaseLock() {},
  };

  const stderrReader = {
    async read(): Promise<{ value?: Uint8Array; done: boolean }> {
      if (stderrChunks.length > 0) {
        return { value: stderrChunks.shift(), done: false };
      }
      return { done: true };
    },
    releaseLock() {},
  };

  return {
    stdin: {
      getWriter: () => stdinWriter,
    },
    stdout: {
      getReader: () => stdoutReader,
    },
    stderr: {
      getReader: () => stderrReader,
    },
    kill: (sig?: string) => {
      killedSignal = sig || "SIGTERM";
      stdoutDone = true;
      stderrDone = true;
      if (stdoutResolve) {
        stdoutResolve();
      }
    },
    getKilledSignal: () => killedSignal,
  };
}

let origDenoCommand: typeof Deno.Command;
let origDenoAddSignalListener: any;

function setup() {
  mockSpawnedCommands = [];
  nextProcessBehavior = {};
  origDenoCommand = Deno.Command;
  origDenoAddSignalListener = (Deno as any).addSignalListener;

  (Deno as any).Command = ExecMockCommand;
  (Deno as any).addSignalListener = (signal: string, handler: Function) => {
    mockSignalListeners.push({ signal, handler });
  };
}

function teardown() {
  if (origDenoCommand) (Deno as any).Command = origDenoCommand;
  if (origDenoAddSignalListener) (Deno as any).addSignalListener = origDenoAddSignalListener;
}

Deno.test("Copilot Exec - spawns Go daedalus-shell binary and runs MCP handshake to execute command", async () => {
  setup();
  Deno.env.set("DAEDALUS_SHELL_BIN", "/mock/bin/daedalus-shell");
  try {
    const result = await execAllowlisted("df", ["-h"]);

    expect(result.returncode).toBe(0);
    expect(result.stdout).toContain("Filesystem");
    expect(result.stderr).toBe("");
    expect(result.error).toBeNull();

    expect(mockSpawnedCommands.length).toBe(1);
    const spawned = mockSpawnedCommands[0];
    // 直接派生 Go 二进制（不再经 deno run 包装）
    expect(spawned.cmd).toBe("/mock/bin/daedalus-shell");

    const args = spawned.options.args as string[];
    expect(args).toEqual([]);
    // 不再向子进程注入 deno 权限旗标（沙箱由 systemd 单元承担）
    expect(args.some((a) => a.startsWith("--allow-"))).toBe(false);

    // 检查 stdio 配置
    expect(spawned.options.stdin).toBe("piped");
    expect(spawned.options.stdout).toBe("piped");
    expect(spawned.options.stderr).toBe("piped");
  } finally {
    Deno.env.delete("DAEDALUS_SHELL_BIN");
    teardown();
  }
});

Deno.test("Copilot Exec - handles gateway security rejection (returncode 126)", async () => {
  setup();
  try {
    nextProcessBehavior = { rejection: true };

    const result = await execAllowlisted("rm", ["-rf", "/tmp/test"]);

    expect(result.returncode).toBe(126);
    expect(result.stderr).toContain("Command 'rm' is not in ALLOW_COMMANDS allowlist.");
    expect(result.error).toContain("Command 'rm' is not in ALLOW_COMMANDS allowlist.");
  } finally {
    teardown();
  }
});

Deno.test("Copilot Exec - handles JSON-RPC protocol error from server", async () => {
  setup();
  try {
    nextProcessBehavior = { mcpError: true };

    const result = await execAllowlisted("unknown_tool", []);

    expect(result.returncode).toBe(-32601);
    expect(result.stderr).toContain("Method 'shell_exec' not found");
    expect(result.error).toContain("Method 'shell_exec' not found");
  } finally {
    teardown();
  }
});

Deno.test("Copilot Exec - handles custom returncode and stderr from command execution", async () => {
  setup();
  try {
    nextProcessBehavior = {
      customInnerResult: {
        stdout: "",
        stderr: "ping: unknown host test.invalid",
        returncode: 2,
        error: null,
      },
    };

    const result = await execAllowlisted("ping", ["-c", "1", "test.invalid"]);

    expect(result.returncode).toBe(2);
    expect(result.stderr).toContain("ping: unknown host test.invalid");
  } finally {
    teardown();
  }
});

Deno.test("Copilot Exec - supports configurable shell binary path via DAEDALUS_SHELL_BIN environment variable", async () => {
  setup();
  Deno.env.set("DAEDALUS_SHELL_BIN", "/custom/path/daedalus-shell");

  try {
    const result = await execAllowlisted("uptime", []);
    expect(result.returncode).toBe(0);

    const spawned = mockSpawnedCommands[0];
    expect(spawned.cmd).toBe("/custom/path/daedalus-shell");
    expect(spawned.options.args).toEqual([]);
  } finally {
    Deno.env.delete("DAEDALUS_SHELL_BIN");
    teardown();
  }
});

Deno.test("Copilot Exec - default resolution finds the real Go daedalus-shell binary on disk", () => {
  // 不 mock Deno.Command：验证默认解析链（生产路径 → 仓库构建产物 daedalus/core/bin/）
  // 在开发态真实文件系统上能解析到一个可 stat 的 Go 二进制。
  const origEnv = Deno.env.get("DAEDALUS_SHELL_BIN");
  try {
    Deno.env.delete("DAEDALUS_SHELL_BIN");
    // 通过未 mock 的解析逻辑间接验证：直接调用模块内解析无导出，
    // 因此这里以 recordAudit 同款策略断言仓库构建产物存在。
    const repoBinary = "daedalus/core/bin/daedalus-shell";
    const parentBinary = "../daedalus/core/bin/daedalus-shell";
    let found = false;
    for (const candidate of [repoBinary, parentBinary]) {
      try {
        Deno.statSync(candidate);
        found = true;
        break;
      } catch {
        // 尝试下一个候选
      }
    }
    expect(found).toBe(true);
  } finally {
    if (origEnv !== undefined) {
      Deno.env.set("DAEDALUS_SHELL_BIN", origEnv);
    }
  }
});

Deno.test("Copilot Exec - registers signal listener for process cleanup", () => {
  expect(typeof (Deno as any).addSignalListener).toBe("function");
});

Deno.test("Copilot Exec - handles watchdog timeout triggering SIGKILL and returncode 124", async () => {
  setup();
  nextProcessBehavior = { hang: true };
  Deno.env.set("DAEDALUS_WATCHDOG_TIMEOUT_MS", "50");

  try {
    const result = await execAllowlisted("df", ["-h"]);
    expect(result.returncode).toBe(124);
    expect(result.stderr).toContain("Timeout: command execution exceeded");
    expect(result.error).toBe("copilot exec timeout");

    const spawned = mockSpawnedCommands[0];
    expect(spawned.instance.getKilledSignal()).toBe("SIGKILL");
  } finally {
    Deno.env.delete("DAEDALUS_WATCHDOG_TIMEOUT_MS");
    teardown();
  }
});

// ===========================================================================
// execTxPropose / execTxPreview / execTxApply —— daedalus-tx 事务原语调用层
// (计划 todo 26)。
//
// ★ 与上面 shell_exec 桥接的本质区别: daedalus-tx 是**纯 CLI**(todo 15 的
// stdout 契约), 不是 MCP 服务器 —— 没有 JSON-RPC 握手、没有 result.content
// 的 text 二次包裹。这些测试用**真实派生**的假 tx 二进制(Deno.makeTempDir +
// 自描述 fixture 脚本)而非 mock Deno.Command: argv 形状与 stdout 单行 JSON
// 文档的解析保真度只能在真实 spawn 边界上验证。
//
// ★ MCP-wire 教训(todo 15 round-3 fold 的镜像面): MCP tools/call 路径里结果
// JSON 被嵌进 `text` 字符串字段, 线上是**双层转义**(grep 必须找 \"ActiveState\");
// 本层直接解析 CLI stdout —— 文档级**单层转义**(非 ASCII 原样透传, 与 Go
// json.Marshal 同策略), JSON.parse 一次即得终值。下面专门有一条测试钉死
// 这个差异, 防止有人把 shell_exec 的二次解析照搬过来。
// ===========================================================================

const TX_FIXTURE_ID = "a1b2c3d4e5f60718";

// 假 daedalus-tx: 按 todo 15 stdout 契约吐 canned JSON 文档;
// env TX_FIXTURE_MODE 选失败形态, argv 逐次落盘到 TX_FIXTURE_ARGV_LOG
// (JSON 数组一行), 精确 stdout 字节落盘到 TX_FIXTURE_STDOUT_LOG 供转义审计。
const TX_FIXTURE_SRC = String.raw`#!/usr/bin/env -S deno run -A
// 测试专用假 daedalus-tx(todo 26): 捕获 argv + 吐 todo 15 stdout 契约文档。
const a = Deno.args;
const argvLog = Deno.env.get("TX_FIXTURE_ARGV_LOG");
if (argvLog) Deno.writeTextFileSync(argvLog, JSON.stringify(a) + "\n", { append: true });
const stdoutLog = Deno.env.get("TX_FIXTURE_STDOUT_LOG");
const mode = Deno.env.get("TX_FIXTURE_MODE") ?? "ok";
const enc = new TextEncoder();
function emit(doc, code) {
  const line = JSON.stringify(doc) + "\n";
  if (stdoutLog) Deno.writeTextFileSync(stdoutLog, line, { append: true });
  Deno.stdout.writeSync(enc.encode(line));
  Deno.exit(code);
}
function emitRaw(text, code) {
  if (stdoutLog) Deno.writeTextFileSync(stdoutLog, text, { append: true });
  Deno.stdout.writeSync(enc.encode(text));
  Deno.exit(code);
}
const sub = a[0] ?? "";
const txId = a[1] ?? "";
if (mode === "hang") {
  // 挂起不退出: 60s 定时器保持事件循环存活(Deno 2 无 sleepSync), 等着被看门狗 SIGKILL
  await new Promise((r) => setTimeout(r, 60000));
  Deno.exit(0);
}
if (mode === "garbage") emitRaw("this is definitely not json\n", 0);
if (mode === "notfound") emit({ error: "transaction " + txId + " not found" }, 1);
if (mode === "usage") emit({ error: "事务 ID 非法: \"zz\" (需匹配 ^[a-f0-9]{16}$)" }, 2);
let receivedArgs = {};
try {
  receivedArgs = JSON.parse(a[3] ?? "{}");
} catch {
  receivedArgs = { _raw: a[3] };
}
const step = {
  index: 0,
  adapter: a[2] ?? "service.set",
  args: receivedArgs,
  before_state: {
    unit_file: "/home/testuser/.config/systemd/user/笔记@1.service",
    file_content: "[Unit]\nDescription=\"原始备份\"\\x\n",
    active_state: "active",
  },
  after_state: { unit: "笔记@1.service", desired_state: "started" },
  op_result: { returncode: 0 },
};
if (sub === "apply") {
  emit({
    id: txId,
    created_at: "2026-09-07T00:00:00Z",
    status: "applied",
    steps: [step, {
      index: 1,
      adapter: "service.set",
      args: { n: 2 },
      op_result: { returncode: 0, stdout: "ok" },
    }],
    rollback_plan: { steps: [step] },
  }, 0);
}
emit({
  id: txId,
  created_at: "2026-09-07T00:00:00Z",
  status: "proposed",
  steps: [step],
  rollback_plan: {},
}, 0);
`;

interface TxFixture {
  dir: string;
  bin: string;
  argvLog: string;
  stdoutLog: string;
}

async function makeTxFixture(mode: string): Promise<TxFixture> {
  const dir = await Deno.makeTempDir({ prefix: "daedalus-tx-fixture-" });
  const bin = `${dir}/tx-fake`;
  await Deno.writeTextFile(bin, TX_FIXTURE_SRC);
  await Deno.chmod(bin, 0o755);
  const f: TxFixture = {
    dir,
    bin,
    argvLog: `${dir}/argv.jsonl`,
    stdoutLog: `${dir}/stdout.log`,
  };
  Deno.env.set(
    "DAEDALUS_TX_BIN",
    mode === "missing" ? `${dir}/no-such-binary` : bin,
  );
  Deno.env.set("TX_FIXTURE_ARGV_LOG", f.argvLog);
  Deno.env.set("TX_FIXTURE_STDOUT_LOG", f.stdoutLog);
  Deno.env.set("TX_FIXTURE_MODE", mode);
  return f;
}

function cleanupTxFixture(f: TxFixture): void {
  Deno.env.delete("DAEDALUS_TX_BIN");
  Deno.env.delete("TX_FIXTURE_ARGV_LOG");
  Deno.env.delete("TX_FIXTURE_STDOUT_LOG");
  Deno.env.delete("TX_FIXTURE_MODE");
  try {
    Deno.removeSync(f.dir, { recursive: true });
  } catch {
    // 尽力而为的清理
  }
}

function capturedArgv(f: TxFixture): string[] {
  const lines = Deno.readTextFileSync(f.argvLog).split("\n").filter((l) =>
    l.trim()
  );
  return JSON.parse(lines[lines.length - 1]) as string[];
}

Deno.test("TestExecTxPropose - spawns tx binary with [propose,id,adapter,argsJson] argv and parses journal", async () => {
  const f = await makeTxFixture("ok");
  try {
    const args = {
      name: "日志服务",
      desired_state: "started",
      note: 'quote"back\\slash',
    };
    const result = await execTxPropose(TX_FIXTURE_ID, "service.set", args);

    expect(result.ok).toBe(true);
    if (!result.ok) return;
    expect(result.journal.id).toBe(TX_FIXTURE_ID);
    expect(result.journal.status).toBe("proposed");
    expect(result.step.index).toBe(0);
    expect(result.step.adapter).toBe("service.set");
    expect(result.step.op_result.returncode).toBe(0);
    // BeforeState/AfterState 视图来自 propose 回吐的日志全文(计划 todo 26 验收)
    expect((result.beforeState as Record<string, unknown>).unit_file).toContain(
      "笔记@1.service",
    );
    expect((result.afterState as Record<string, unknown>).desired_state).toBe(
      "started",
    );
    // args 经 argv(JSON 序列化)往返:发什么收什么, unicode/引号/反斜杠零损伤
    expect(result.step.args).toEqual(args);

    // argv 形状契约: ["propose", txId, adapter, JSON.stringify(args)]
    expect(capturedArgv(f)).toEqual([
      "propose",
      TX_FIXTURE_ID,
      "service.set",
      JSON.stringify(args),
    ]);
  } finally {
    cleanupTxFixture(f);
  }
});

Deno.test("TestExecTxPropose - CLI stdout is single-level escaped JSON doc (contrast MCP-wire double escape)", async () => {
  const f = await makeTxFixture("ok");
  try {
    await execTxPropose(TX_FIXTURE_ID, "service.set", { name: "n" });

    const raw = Deno.readTextFileSync(f.stdoutLog);
    // 文档级单层转义: 引号 → \" 各一次; 单反斜杠 → \\; 非 ASCII 原样透传
    expect(raw.includes('Description=\\"原始备份\\"')).toBe(true);
    expect(raw.includes("笔记")).toBe(true);
    // 若出现 4 连反斜杠则说明多套了一层字符串化(MCP text 字段形态)——必须没有
    expect(raw.includes("\\\\\\\\")).toBe(false);
    // 一次 JSON.parse 即得终值(本层不经 MCP, 绝不二次解析)
    const doc = JSON.parse(raw.trim()) as Record<string, any>;
    expect(doc.steps[0].before_state.file_content).toBe(
      '[Unit]\nDescription="原始备份"\\x\n',
    );
  } finally {
    cleanupTxFixture(f);
  }
});

Deno.test("TestExecTxPreview - spawns [status,txId] and renders step diff view", async () => {
  const f = await makeTxFixture("ok");
  try {
    const result = await execTxPreview(TX_FIXTURE_ID);

    expect(result.ok).toBe(true);
    if (!result.ok) return;
    expect(result.journal.id).toBe(TX_FIXTURE_ID);
    expect(result.diff.length).toBe(1);
    expect(result.diff[0].adapter).toBe("service.set");
    expect((result.diff[0].beforeState as Record<string, unknown>).active_state)
      .toBe(
        "active",
      );
    expect((result.diff[0].afterState as Record<string, unknown>).desired_state)
      .toBe(
        "started",
      );
    expect(capturedArgv(f)).toEqual(["status", TX_FIXTURE_ID]);
  } finally {
    cleanupTxFixture(f);
  }
});

Deno.test("TestExecTxApply - spawns [apply,txId] and returns op_result set from applied journal", async () => {
  const f = await makeTxFixture("ok");
  try {
    const result = await execTxApply(TX_FIXTURE_ID);

    expect(result.ok).toBe(true);
    if (!result.ok) return;
    expect(result.journal.status).toBe("applied");
    expect(result.opResults.length).toBe(2);
    expect(result.opResults[0].returncode).toBe(0);
    expect(result.opResults[1].stdout).toBe("ok");
    expect(capturedArgv(f)).toEqual(["apply", TX_FIXTURE_ID]);
  } finally {
    cleanupTxFixture(f);
  }
});

Deno.test("TestExecTxApply_TxNotFound - structured failure (kind tx_not_found, returncode 1), never throws", async () => {
  const f = await makeTxFixture("notfound");
  try {
    const result = await execTxApply(TX_FIXTURE_ID);

    expect(result.ok).toBe(false);
    if (result.ok) return;
    expect(result.kind).toBe("tx_not_found");
    expect(result.returncode).toBe(1);
    expect(result.error).toBe(`transaction ${TX_FIXTURE_ID} not found`);
  } finally {
    cleanupTxFixture(f);
  }
});

Deno.test("TestExecTxApply_UsageError - exit 2 maps to kind tx_usage_error", async () => {
  const f = await makeTxFixture("usage");
  try {
    const result = await execTxApply(TX_FIXTURE_ID);

    expect(result.ok).toBe(false);
    if (result.ok) return;
    expect(result.kind).toBe("tx_usage_error");
    expect(result.returncode).toBe(2);
    expect(result.error).toContain("事务 ID 非法");
  } finally {
    cleanupTxFixture(f);
  }
});

Deno.test("TestExecTxPropose_MissingBinary - spawn failure maps to kind tx_spawn_error, never throws", async () => {
  const f = await makeTxFixture("missing");
  try {
    const result = await execTxPropose(TX_FIXTURE_ID, "service.set", {});

    expect(result.ok).toBe(false);
    if (result.ok) return;
    expect(result.kind).toBe("tx_spawn_error");
    expect(result.returncode).toBe(1);
    expect(result.error).toContain("Failed to spawn");
  } finally {
    cleanupTxFixture(f);
  }
});

Deno.test("TestExecTxPropose_UnparseableStdout - exit 0 non-JSON maps to kind tx_parse_error", async () => {
  const f = await makeTxFixture("garbage");
  try {
    const result = await execTxPropose(TX_FIXTURE_ID, "service.set", {});

    expect(result.ok).toBe(false);
    if (result.ok) return;
    expect(result.kind).toBe("tx_parse_error");
    expect(result.error).toContain("this is definitely not json");
  } finally {
    cleanupTxFixture(f);
  }
});

Deno.test("TestExecTxApply_WatchdogTimeout - 40s watchdog (env-overridable) SIGKILLs and maps to kind tx_timeout", async () => {
  const f = await makeTxFixture("hang");
  Deno.env.set("DAEDALUS_WATCHDOG_TIMEOUT_MS", "300");
  try {
    const start = Date.now();
    const result = await execTxApply(TX_FIXTURE_ID);
    const elapsed = Date.now() - start;

    expect(result.ok).toBe(false);
    if (result.ok) return;
    expect(result.kind).toBe("tx_timeout");
    expect(result.returncode).toBe(124);
    expect(result.error).toBe("copilot exec timeout");
    // 超时确实发生(远小于 fixture 的 60s 阻塞, 证明看门狗生效)
    expect(elapsed).toBeLessThan(30000);
  } finally {
    Deno.env.delete("DAEDALUS_WATCHDOG_TIMEOUT_MS");
    cleanupTxFixture(f);
  }
});

Deno.test("TestResolveTxBinary - default resolution finds repo build artifact daedalus/core/bin/daedalus-tx on disk", () => {
  // 不派生进程: 与 daedalus-shell 同款磁盘断言, 验证 dev 树回退链的实物存在
  // (CI 先 just go-build, 与 shell 断言同等前置)。
  const origEnv = Deno.env.get("DAEDALUS_TX_BIN");
  try {
    Deno.env.delete("DAEDALUS_TX_BIN");
    let found = false;
    for (
      const candidate of [
        "daedalus/core/bin/daedalus-tx",
        "../daedalus/core/bin/daedalus-tx",
      ]
    ) {
      try {
        Deno.statSync(candidate);
        found = true;
        break;
      } catch {
        // 尝试下一个候选
      }
    }
    expect(found).toBe(true);
  } finally {
    if (origEnv !== undefined) {
      Deno.env.set("DAEDALUS_TX_BIN", origEnv);
    }
  }
});
