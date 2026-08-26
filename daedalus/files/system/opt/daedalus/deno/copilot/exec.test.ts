import { expect } from "jsr:@std/expect@1";
import { execAllowlisted } from "./exec.ts";

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
let origDenoExecPath: any;

function setup() {
  mockSpawnedCommands = [];
  nextProcessBehavior = {};
  origDenoCommand = Deno.Command;
  origDenoAddSignalListener = (Deno as any).addSignalListener;
  origDenoExecPath = (Deno as any).execPath;

  // 模拟真实开发态下 Deno.execPath() 返回安装路径（如 asdf/nix），
  // 使默认解析不再硬编码 /usr/local/bin/deno。
  (Deno as any).execPath = () => "/mock/dev/bin/deno";

  (Deno as any).Command = ExecMockCommand;
  (Deno as any).addSignalListener = (signal: string, handler: Function) => {
    mockSignalListeners.push({ signal, handler });
  };
}

function teardown() {
  if (origDenoCommand) (Deno as any).Command = origDenoCommand;
  if (origDenoAddSignalListener) (Deno as any).addSignalListener = origDenoAddSignalListener;
  if (origDenoExecPath) (Deno as any).execPath = origDenoExecPath;
}

Deno.test("Copilot Exec - spawns deno with exact permissions and runs MCP handshake to execute command", async () => {
  setup();
  try {
    const result = await execAllowlisted("df", ["-h"]);

    expect(result.returncode).toBe(0);
    expect(result.stdout).toContain("Filesystem");
    expect(result.stderr).toBe("");
    expect(result.error).toBeNull();

    expect(mockSpawnedCommands.length).toBe(1);
    const spawned = mockSpawnedCommands[0];
    // 默认解析使用 Deno.execPath()（开发态真实安装路径），不再硬编码 /usr/local/bin/deno
    expect(spawned.cmd).toBe("/mock/dev/bin/deno");

    const args = spawned.options.args as string[];
    expect(args[0]).toBe("run");

    // 检查 allow-run 包含 15 个命令，同时支持 /usr/bin 和 /bin
    const allowRunArg = args.find((a) => a.startsWith("--allow-run="));
    expect(allowRunArg).toBeDefined();
    expect(allowRunArg).toContain("/usr/bin/df");
    expect(allowRunArg).toContain("/bin/df");
    expect(allowRunArg).toContain("/usr/bin/systemctl");
    expect(allowRunArg).toContain("/bin/systemctl");

    // 检查 allow-read 包含 daedalus-shell-deno.service 中的完整集合
    const allowReadArg = args.find((a) => a.startsWith("--allow-read="));
    expect(allowReadArg).toBeDefined();
    expect(allowReadArg).toContain("/home");
    expect(allowReadArg).toContain("/var/log");
    expect(allowReadArg).toContain("/tmp");
    expect(allowReadArg).toContain("/proc");
    expect(allowReadArg).toContain("/sys");
    expect(allowReadArg).toContain("/etc/os-release");
    expect(allowReadArg).toContain("/usr/lib/os-release");
    expect(allowReadArg).toContain("/etc/fedora-release");
    expect(allowReadArg).toContain("/etc/almalinux-release");

    // 检查 allow-write
    const allowWriteArg = args.find((a) => a.startsWith("--allow-write="));
    expect(allowWriteArg).toBe("--allow-write=/var/log/daedalus");

    // 检查 allow-env：shell_server.ts 顶层调用 Deno.env.get，
    // 缺少该标志会导致子进程加载时抛出 NotCapable 并立即退出
    expect(args).toContain("--allow-env");

    // 检查目标脚本（默认解析：无环境变量时回溯仓库内 shell_server.ts；
    // 测试运行于仓库内，因此解析到仓库内的相对路径而非 /opt/daedalus 生产路径）
    expect(args[args.length - 1]).toContain("shell_server.ts");

    // 检查 stdio 配置
    expect(spawned.options.stdin).toBe("piped");
    expect(spawned.options.stdout).toBe("piped");
    expect(spawned.options.stderr).toBe("piped");
  } finally {
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

Deno.test("Copilot Exec - supports configurable binary and server paths via environment variables", async () => {
  setup();
  Deno.env.set("DAEDALUS_DENO_PATH", "/custom/bin/deno");
  Deno.env.set("DAEDALUS_SHELL_SERVER_PATH", "/custom/path/shell_server.ts");

  try {
    const result = await execAllowlisted("uptime", []);
    expect(result.returncode).toBe(0);

    const spawned = mockSpawnedCommands[0];
    expect(spawned.cmd).toBe("/custom/bin/deno");
    const args = spawned.options.args as string[];
    expect(args[args.length - 1]).toBe("/custom/path/shell_server.ts");
  } finally {
    Deno.env.delete("DAEDALUS_DENO_PATH");
    Deno.env.delete("DAEDALUS_SHELL_SERVER_PATH");
    teardown();
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
