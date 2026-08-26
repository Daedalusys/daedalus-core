import { expect } from "jsr:@std/expect@1";
import {
  isPathLike,
  validatePath,
  validateArg,
  validateCommand,
  handleMessage,
  shellExec,
} from "./shell_server.ts";

Deno.test("Deno Shell Server Logic - validateCommand accepts allowed commands and rejects disallowed commands", () => {
  expect(validateCommand("df")).toBe("df");
  expect(validateCommand("ls")).toBe("ls");
  expect(validateCommand("cat")).toBe("cat");
  expect(validateCommand("uname")).toBe("uname");
  expect(validateCommand("/usr/bin/df")).toBe("df");

  expect(() => validateCommand("rm")).toThrow();
  expect(() => validateCommand("bash")).toThrow();
  expect(() => validateCommand("python3")).toThrow();
  expect(() => validateCommand("curl")).toThrow();
  expect(() => validateCommand("cat\0")).toThrow();
});

Deno.test("Deno Shell Server Logic - isPathLike correctly detects paths", () => {
  expect(isPathLike("/home/user")).toBe(true);
  expect(isPathLike("rel/path")).toBe(true);
  expect(isPathLike(".")).toBe(true);
  expect(isPathLike("..")).toBe(true);
  expect(isPathLike("-h")).toBe(false);
  expect(isPathLike("test")).toBe(false);
});

Deno.test("Deno Shell Server Logic - validatePath allows whitelisted paths and forbids blocked / outside paths", () => {
  expect(validatePath("/home")).toBe("/home");
  expect(validatePath("/tmp")).toBe("/tmp");
  expect(validatePath("/var/log")).toBe("/var/log");
  // /etc/os-release 在 systemd 系统上解析为 /usr/lib/os-release，两者均被允许
  const osReleaseRes = validatePath("/etc/os-release");
  expect(osReleaseRes === "/etc/os-release" || osReleaseRes === "/usr/lib/os-release").toBe(true);

  // 受阻/禁止路径
  expect(() => validatePath("/etc/shadow")).toThrow();
  expect(() => validatePath("/root")).toThrow();
  expect(() => validatePath("/etc/sudoers")).toThrow();
  expect(() => validatePath("/home/user/..\0/etc")).toThrow();

  // 允许范围外的路径
  expect(() => validatePath("/boot")).toThrow();
  expect(() => validatePath("/usr/local")).toThrow();
});

Deno.test("Deno Shell Server Logic - validateArg catches null bytes and illegal paths in flags", () => {
  expect(() => validateArg("--file=/etc/shadow")).toThrow();
  expect(() => validateArg("/etc/shadow")).toThrow();
  expect(() => validateArg("bad\0arg")).toThrow();
  expect(() => validateArg("-h")).not.toThrow();
  expect(() => validateArg("--output=/tmp/out.txt")).not.toThrow();
});

Deno.test("Deno Shell Server Logic - handleMessage JSON-RPC initialize and tools/list", async () => {
  const initRes = await handleMessage({
    jsonrpc: "2.0",
    id: 1,
    method: "initialize",
    params: {},
  });
  expect(initRes?.id).toBe(1);
  expect((initRes?.result as any).serverInfo.name).toBe("daedalus-shell-deno");

  const listRes = await handleMessage({
    jsonrpc: "2.0",
    id: 2,
    method: "tools/list",
    params: {},
  });
  expect(listRes?.id).toBe(2);
  const tools = (listRes?.result as any).tools;
  expect(tools.length).toBe(1);
  expect(tools[0].name).toBe("shell_exec");
});

Deno.test("Deno Shell Server Logic - handleMessage tools/call shell_exec happy path", async () => {
  const callRes = await handleMessage({
    jsonrpc: "2.0",
    id: 3,
    method: "tools/call",
    params: {
      name: "shell_exec",
      arguments: {
        command: "df",
        args: ["-h"],
      },
    },
  });
  expect(callRes?.id).toBe(3);
  const content = (callRes?.result as any).content[0].text;
  const parsed = JSON.parse(content);
  expect(parsed.returncode).toBe(0);
  expect(parsed.stdout).toContain("Filesystem");
});

Deno.test("Deno Shell Server Logic - handleMessage tools/call shell_exec forbidden command", async () => {
  const callRes = await handleMessage({
    jsonrpc: "2.0",
    id: 4,
    method: "tools/call",
    params: {
      name: "shell_exec",
      arguments: {
        command: "rm",
        args: ["-rf", "/"],
      },
    },
  });
  expect(callRes?.id).toBe(4);
  const content = (callRes?.result as any).content[0].text;
  const parsed = JSON.parse(content);
  expect(parsed.returncode).toBe(126);
  expect((callRes?.result as any).isError).toBe(true);
});

Deno.test("Deno Shell Server Logic - handleMessage tools/call shell_exec forbidden path argument", async () => {
  const callRes = await handleMessage({
    jsonrpc: "2.0",
    id: 5,
    method: "tools/call",
    params: {
      name: "shell_exec",
      arguments: {
        command: "cat",
        args: ["/etc/shadow"],
      },
    },
  });
  expect(callRes?.id).toBe(5);
  const content = (callRes?.result as any).content[0].text;
  const parsed = JSON.parse(content);
  expect(parsed.returncode).toBe(126);
  expect(parsed.stderr).toContain("blocked path");
  expect((callRes?.result as any).isError).toBe(true);
});
