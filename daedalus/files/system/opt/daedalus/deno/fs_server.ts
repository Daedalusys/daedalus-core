#!/usr/bin/env -S deno run
/**
 * daedalus-fs (Deno): 模型上下文协议 (MCP) 文件系统服务器
 *
 * 为文件系统操作实现独立的 JSON-RPC 2.0 stdio MCP 服务器。
 * 设计为在 Deno 细粒度运行时权限下安全运行：
 *   deno run --allow-read=/home,/var/log,/tmp --allow-write=/home,/tmp --allow-net=localhost fs_server.ts
 *
 * 支持的工具：
 *   - read_file({ path: string }) -> string
 *   - write_file({ path: string, content: string }) -> string
 *   - list_dir({ path: string }) -> string[]
 *   - move_file({ src: string, dst: string }) -> string
 */

// 针对未预装 Deno 全局对象的环境声明 Deno 命名空间类型
declare const Deno: {
  stdin: {
    readable: ReadableStream<Uint8Array>;
  };
  stdout: {
    write(p: Uint8Array): Promise<number>;
  };
  readTextFile(path: string | URL): Promise<string>;
  writeTextFile(path: string | URL, data: string): Promise<void>;
  readDir(path: string | URL): AsyncIterable<{ name: string; isFile: boolean; isDirectory: boolean; isSymlink: boolean }>;
  rename(oldPath: string, newPath: string): Promise<void>;
  stat(path: string | URL): Promise<{ isFile: boolean; isDirectory: boolean; isSymlink: boolean }>;
  mkdir(path: string | URL, options?: { recursive?: boolean }): Promise<void>;
  realPath(path: string): Promise<string>;
  env: {
    get(key: string): string | undefined;
  };
};

export const ALLOWED_DIRS: string[] = ["/home", "/var/log", "/tmp"];

/**
 * 依据白名单和禁止模式验证路径。
 * 确保：
 * 1. 非空字符串且不含空字节。
 * 2. 以 '/' 开头的绝对路径。
 * 3. 解析规范化路径并依据 ALLOWED_DIRS 验证前缀。
 */
export async function validatePath(pathStr: string, _write: boolean = false): Promise<string> {
  if (!pathStr || typeof pathStr !== "string") {
    throw new Error("Path must be a non-empty string.");
  }

  if (pathStr.includes("\0")) {
    throw new Error("Invalid path: null bytes are forbidden.");
  }

  if (!pathStr.startsWith("/")) {
    throw new Error(`Invalid path '${pathStr}': only absolute paths are permitted.`);
  }

  // 尽可能通过 Deno.realPath 规范化路径，若失败则回退至手动规范化
  let canonicalPath: string;
  try {
    canonicalPath = await Deno.realPath(pathStr);
  } catch (_e) {
    // 若目标文件尚不存在（例如用于 write_file 或 move 目标），
    // 则规范化路径并尽可能解析父路径
    canonicalPath = normalizePath(pathStr);
  }

  let isAllowed = false;
  for (const allowed of ALLOWED_DIRS) {
    let allowedCanonical = allowed;
    try {
      allowedCanonical = await Deno.realPath(allowed);
    } catch (_e) {
      allowedCanonical = normalizePath(allowed);
    }

    const cleanAllowed = allowedCanonical.replace(/\/+$/, "");
    if (canonicalPath === cleanAllowed || canonicalPath.startsWith(cleanAllowed + "/")) {
      isAllowed = true;
      break;
    }
  }

  if (!isAllowed) {
    throw new Error(
      `Access denied: path '${pathStr}' (resolved: '${canonicalPath}') is outside allowed directories (${ALLOWED_DIRS.join(", ")}).`
    );
  }

  return canonicalPath;
}

/**
 * 解析 '.' 和 '..' 的 POSIX 路径规范化辅助函数
 */
export function normalizePath(path: string): string {
  const parts = path.split("/").filter(Boolean);
  const stack: string[] = [];

  for (const part of parts) {
    if (part === ".") continue;
    if (part === "..") {
      stack.pop();
    } else {
      stack.push(part);
    }
  }

  return "/" + stack.join("/");
}

/**
 * 工具：read_file
 */
export async function readFileTool(args: { path: string }): Promise<string> {
  if (!args || typeof args.path !== "string") {
    throw new Error("Missing required argument 'path' (string).");
  }

  const safePath = await validatePath(args.path, false);
  const info = await Deno.stat(safePath);
  if (info.isDirectory) {
    throw new Error(`Target is a directory, not a file: ${args.path}`);
  }

  return await Deno.readTextFile(safePath);
}

/**
 * 工具：write_file
 */
export async function write_fileTool(args: { path: string; content: string }): Promise<string> {
  if (!args || typeof args.path !== "string" || typeof args.content !== "string") {
    throw new Error("Missing required arguments 'path' (string) and 'content' (string).");
  }

  const safePath = await validatePath(args.path, true);

  try {
    const info = await Deno.stat(safePath);
    if (info.isDirectory) {
      throw new Error(`Target is a directory: ${args.path}`);
    }
  } catch (_e) {
    // 文件尚不存在，符合预期
  }

  const parentDir = safePath.substring(0, safePath.lastIndexOf("/"));
  if (parentDir && parentDir !== "") {
    try {
      await Deno.mkdir(parentDir, { recursive: true });
    } catch (_e) {
      // 若已存在则忽略
    }
  }

  await Deno.writeTextFile(safePath, args.content);
  return `Successfully wrote ${args.content.length} characters to ${args.path}`;
}

/**
 * 工具：list_dir
 */
export async function listDirTool(args: { path: string }): Promise<string[]> {
  if (!args || typeof args.path !== "string") {
    throw new Error("Missing required argument 'path' (string).");
  }

  const safePath = await validatePath(args.path, false);
  const info = await Deno.stat(safePath);
  if (!info.isDirectory) {
    throw new Error(`Target is not a directory: ${args.path}`);
  }

  const entries: string[] = [];
  for await (const entry of Deno.readDir(safePath)) {
    entries.push(entry.name);
  }

  return entries.sort();
}

/**
 * 工具：move_file
 */
export async function moveFileTool(args: { src: string; dst: string }): Promise<string> {
  if (!args || typeof args.src !== "string" || typeof args.dst !== "string") {
    throw new Error("Missing required arguments 'src' (string) and 'dst' (string).");
  }

  const safeSrc = await validatePath(args.src, true);
  const safeDst = await validatePath(args.dst, true);

  const dstParent = safeDst.substring(0, safeDst.lastIndexOf("/"));
  if (dstParent && dstParent !== "") {
    try {
      await Deno.mkdir(dstParent, { recursive: true });
    } catch (_e) {
      // 若已存在则忽略
    }
  }

  await Deno.rename(safeSrc, safeDst);
  return `Successfully moved ${args.src} to ${args.dst}`;
}

/**
 * MCP 工具定义
 */
export const MCP_TOOLS = [
  {
    name: "read_file",
    description: "Read text content from a file within allowed directories (/home, /var/log, /tmp).",
    inputSchema: {
      type: "object",
      properties: {
        path: {
          type: "string",
          description: "Absolute path to the file to read.",
        },
      },
      required: ["path"],
    },
    annotations: {
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: false,
    },
  },
  {
    name: "write_file",
    description: "Write or overwrite text content to a file within allowed directories (/home, /var/log, /tmp). Automatically creates parent directories if they do not exist.",
    inputSchema: {
      type: "object",
      properties: {
        path: {
          type: "string",
          description: "Absolute path to the destination file.",
        },
        content: {
          type: "string",
          description: "Text content to write into the file.",
        },
      },
      required: ["path", "content"],
    },
    annotations: {
      readOnlyHint: false,
      destructiveHint: true,
      idempotentHint: true,
      openWorldHint: false,
    },
  },
  {
    name: "list_dir",
    description: "List contents of a directory within allowed directories (/home, /var/log, /tmp).",
    inputSchema: {
      type: "object",
      properties: {
        path: {
          type: "string",
          description: "Absolute path to the directory.",
        },
      },
      required: ["path"],
    },
    annotations: {
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: false,
    },
  },
  {
    name: "move_file",
    description: "Move or rename a file or directory within allowed directories (/home, /var/log, /tmp). Both source and destination must be strictly inside the allowed whitelist.",
    inputSchema: {
      type: "object",
      properties: {
        src: {
          type: "string",
          description: "Absolute path to the source file or directory.",
        },
        dst: {
          type: "string",
          description: "Absolute path to the destination file or directory.",
        },
      },
      required: ["src", "dst"],
    },
    annotations: {
      readOnlyHint: false,
      destructiveHint: true,
      idempotentHint: false,
      openWorldHint: false,
    },
  },
];

interface JsonRpcRequest {
  jsonrpc: string;
  id?: string | number | null;
  method: string;
  params?: any;
}

interface JsonRpcResponse {
  jsonrpc: string;
  id: string | number | null;
  result?: any;
  error?: {
    code: number;
    message: string;
    data?: any;
  };
}

/**
 * 根据 MCP 协议规范处理单个 JSON-RPC 2.0 请求。
 */
export async function handleJsonRpcMessage(req: JsonRpcRequest): Promise<JsonRpcResponse | null> {
  // 如果不是有效的 JSON-RPC 请求结构
  if (!req || typeof req !== "object" || req.jsonrpc !== "2.0") {
    return {
      jsonrpc: "2.0",
      id: req?.id ?? null,
      error: {
        code: -32600,
        message: "Invalid Request: expected jsonrpc='2.0'",
      },
    };
  }

  const { id, method, params } = req;

  // 处理通知（无 ID）
  if (id === undefined || id === null) {
    if (method === "notifications/initialized" || method === "exit") {
      // 静默处理通知
    }
    return null;
  }

  try {
    switch (method) {
      case "initialize": {
        return {
          jsonrpc: "2.0",
          id,
          result: {
            protocolVersion: "2024-11-05",
            capabilities: {
              tools: {
                listChanged: false,
              },
            },
            serverInfo: {
              name: "daedalus-fs",
              version: "1.0.0",
            },
          },
        };
      }

      case "ping": {
        return {
          jsonrpc: "2.0",
          id,
          result: {},
        };
      }

      case "tools/list": {
        return {
          jsonrpc: "2.0",
          id,
          result: {
            tools: MCP_TOOLS,
          },
        };
      }

      case "tools/call": {
        if (!params || typeof params.name !== "string") {
          return {
            jsonrpc: "2.0",
            id,
            error: {
              code: -32602,
              message: "Invalid params: 'name' is required for tools/call.",
            },
          };
        }

        const toolName = params.name;
        const toolArgs = params.arguments || {};

        try {
          let toolResult: any;
          if (toolName === "read_file") {
            toolResult = await readFileTool(toolArgs);
          } else if (toolName === "write_file") {
            toolResult = await write_fileTool(toolArgs);
          } else if (toolName === "list_dir") {
            toolResult = await listDirTool(toolArgs);
          } else if (toolName === "move_file") {
            toolResult = await moveFileTool(toolArgs);
          } else {
            return {
              jsonrpc: "2.0",
              id,
              result: {
                isError: true,
                content: [
                  {
                    type: "text",
                    text: `Unknown tool: ${toolName}`,
                  },
                ],
              },
            };
          }

          const responseText =
            typeof toolResult === "string" ? toolResult : JSON.stringify(toolResult, null, 2);

          return {
            jsonrpc: "2.0",
            id,
            result: {
              content: [
                {
                  type: "text",
                  text: responseText,
                },
              ],
            },
          };
        } catch (err: any) {
          return {
            jsonrpc: "2.0",
            id,
            result: {
              isError: true,
              content: [
                {
                  type: "text",
                  text: `Error: ${err?.message || String(err)}`,
                },
              ],
            },
          };
        }
      }

      default: {
        return {
          jsonrpc: "2.0",
          id,
          error: {
            code: -32601,
            message: `Method not found: ${method}`,
          },
        };
      }
    }
  } catch (error: any) {
    return {
      jsonrpc: "2.0",
      id,
      error: {
        code: -32603,
        message: `Internal error: ${error?.message || String(error)}`,
      },
    };
  }
}

/**
 * 从标准输入按行读取 JSON-RPC 消息的主 stdio 事件循环。
 */
export async function runServer(): Promise<void> {
  const decoder = new TextDecoder();
  const encoder = new TextEncoder();

  let buffer = "";

  const reader = Deno.stdin.readable.getReader();

  try {
    while (true) {
      const { value, done } = await reader.read();
      if (done) break;

      buffer += decoder.decode(value, { stream: true });
      const lines = buffer.split("\n");
      buffer = lines.pop() || "";

      for (const line of lines) {
        const trimmed = line.trim();
        if (!trimmed) continue;

        try {
          const req: JsonRpcRequest = JSON.parse(trimmed);
          const res = await handleJsonRpcMessage(req);
          if (res) {
            const outLine = JSON.stringify(res) + "\n";
            await Deno.stdout.write(encoder.encode(outLine));
          }
        } catch (_parseErr) {
          const parseErrResponse: JsonRpcResponse = {
            jsonrpc: "2.0",
            id: null,
            error: {
              code: -32700,
              message: "Parse error: Invalid JSON received.",
            },
          };
          await Deno.stdout.write(encoder.encode(JSON.stringify(parseErrResponse) + "\n"));
        }
      }
    }
  } finally {
    reader.releaseLock();
  }
}

// 直接执行时自动运行
if (typeof Deno !== "undefined" && import.meta.main) {
  runServer().catch((err) => {
    // 将错误输出至 stderr，以确保 stdout 的 JSON-RPC 数据流保持完整
    console.error("Daedalus-fs server error:", err);
  });
}
