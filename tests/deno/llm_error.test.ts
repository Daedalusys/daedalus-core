/**
 * advisor-robustness:LLM 调用鲁棒性测试(TDD 红先行)。
 *
 * 覆盖(advisor-robustness plan 任务 1+2+3+6):
 * - parseTimeoutMs:DAEDALUS_LLM_TIMEOUT_MS 解析、非法回退 30000、clamp [1000, 300000]
 * - classifyFetchError:4 种错误分类(timeout / http / network / config)
 * - isRetryable:timeout/network/5xx 可重试;4xx(含 408/429)/config 不可重试
 * - callProvider 透明重试流:mock globalThis.fetch 控制调用次数与响应序列,
 *   断言重试只 1 次、最终错误带 kind/fields、重试成功无 error 痕迹
 *
 * 全部纯 mock:不碰真实网络(fetch 被 stub)、不碰 i18n json、不 sleep 真 2s
 * (经 _retryDelayMsForTest 注入口清零)。
 */
import { expect } from "jsr:@std/expect@1";
import { join } from "jsr:@std/path@1";
import {
  _retryDelayMsForTest,
  classifyFetchError,
  isRetryable,
  parseTimeoutMs,
  translate,
  type NormalizedLLMError,
} from "../../daedalus/plugin/copilot/llm.ts";

const originalFetch = globalThis.fetch;
// 集成测试统一走 openai baseUrl=http://127.0.0.1:1/v1 → endpoint 拼接结果(去尾斜杠 + /chat/completions)。
const TEST_ENDPOINT = "http://127.0.0.1:1/v1/chat/completions";
let tempDir = "";

/**
 * 搭建 fetch 集成用例的隔离环境:
 * - DAEDALUS_CONFIG_PATH 指向不存在的临时路径 → 屏蔽宿主机个人配置(hermetic)
 * - API key / BASE_URL 经 env 注入(endpoint 无所谓,fetch 已被 stub)
 * - 超时设 1000ms;重试退避经注入口清零,测试免等真 2s
 */
function setupLlmEnv(): void {
  tempDir = Deno.makeTempDirSync({ prefix: "daedalus-llm-error-test-" });
  Deno.env.set("DAEDALUS_CONFIG_PATH", join(tempDir, "absent", "copilot.json"));
  Deno.env.set("DAEDALUS_LLM_API_KEY", "test-key");
  Deno.env.set("DAEDALUS_LLM_BASE_URL", "http://127.0.0.1:1/v1");
  Deno.env.delete("DAEDALUS_LLM_PROVIDER");
  Deno.env.delete("DAEDALUS_LLM_MODEL");
  Deno.env.set("DAEDALUS_LLM_TIMEOUT_MS", "1000");
  _retryDelayMsForTest.ms = 0;
}

function teardownLlmEnv(): void {
  globalThis.fetch = originalFetch;
  _retryDelayMsForTest.ms = 2_000;
  Deno.env.delete("DAEDALUS_CONFIG_PATH");
  Deno.env.delete("DAEDALUS_LLM_API_KEY");
  Deno.env.delete("DAEDALUS_LLM_BASE_URL");
  Deno.env.delete("DAEDALUS_LLM_TIMEOUT_MS");
  try {
    Deno.removeSync(tempDir, { recursive: true });
  } catch {
    // 忽略清理错误
  }
}

/** 构造合法的 OpenAI 200 响应(content 为字符串即成功提取)。 */
function openaiOk(content: string): Response {
  return new Response(
    JSON.stringify({ choices: [{ message: { content } }] }),
    { status: 200, headers: { "Content-Type": "application/json" } },
  );
}

// ---------------------------------------------------------------------------
// parseTimeoutMs:env 解析与 clamp 边界
// ---------------------------------------------------------------------------

Deno.test("parseTimeoutMs - 未设置环境变量时回退默认 30000", () => {
  Deno.env.delete("DAEDALUS_LLM_TIMEOUT_MS");
  expect(parseTimeoutMs()).toBe(30_000);
});

Deno.test("parseTimeoutMs - 非正数与非数字字符串一律回退默认 30000", () => {
  for (const raw of ["0", "-100", "abc", "", "  "]) {
    Deno.env.set("DAEDALUS_LLM_TIMEOUT_MS", raw);
    expect(parseTimeoutMs()).toBe(30_000);
  }
  Deno.env.delete("DAEDALUS_LLM_TIMEOUT_MS");
});

Deno.test("parseTimeoutMs - 合法值原样透传", () => {
  Deno.env.set("DAEDALUS_LLM_TIMEOUT_MS", "5000");
  expect(parseTimeoutMs()).toBe(5_000);
  Deno.env.set("DAEDALUS_LLM_TIMEOUT_MS", "60000");
  expect(parseTimeoutMs()).toBe(60_000);
  Deno.env.delete("DAEDALUS_LLM_TIMEOUT_MS");
});

Deno.test("parseTimeoutMs - 超大值 clamp 到上界 300000", () => {
  Deno.env.set("DAEDALUS_LLM_TIMEOUT_MS", "999999999");
  expect(parseTimeoutMs()).toBe(300_000);
  Deno.env.delete("DAEDALUS_LLM_TIMEOUT_MS");
});

Deno.test("parseTimeoutMs - 过小值 clamp 到下界 1000", () => {
  Deno.env.set("DAEDALUS_LLM_TIMEOUT_MS", "100");
  expect(parseTimeoutMs()).toBe(1_000);
  Deno.env.delete("DAEDALUS_LLM_TIMEOUT_MS");
});

// ---------------------------------------------------------------------------
// classifyFetchError:4 种错误分类
// ---------------------------------------------------------------------------

Deno.test("classifyFetchError - TimeoutError(DOMException 与普通 Error)归类 timeout", () => {
  const dom = new DOMException("aborted", "TimeoutError");
  const n1 = classifyFetchError(dom, TEST_ENDPOINT, 1000);
  expect(n1.kind).toBe("timeout");
  expect(n1.fields.endpoint).toBe(TEST_ENDPOINT);
  expect(n1.fields.timeoutMs).toBe(1000);

  const plain = new Error("The operation was aborted due to timeout");
  plain.name = "TimeoutError";
  const n2 = classifyFetchError(plain, TEST_ENDPOINT, 1000);
  expect(n2.kind).toBe("timeout");
});

Deno.test("classifyFetchError - TypeError(fetch 网络层)归类 network 且带原始 message", () => {
  const n = classifyFetchError(
    new TypeError("Connect Failed"),
    TEST_ENDPOINT,
    1000,
  );
  expect(n.kind).toBe("network");
  expect(n.fields.endpoint).toBe(TEST_ENDPOINT);
  expect(n.fields.err).toBe("Connect Failed");
});

Deno.test("classifyFetchError - 自带 kind 的自抛错误原样返回(引用相等)", () => {
  const own: NormalizedLLMError & Error = Object.assign(
    new Error("OpenAI API error (503): busy"),
    {
      kind: "http" as const,
      fields: { endpoint: TEST_ENDPOINT, status: 503, body: "busy", timeoutMs: 1000 },
    },
  );
  // 已规范化的错误不得被再分类/包装,直接透传
  expect(classifyFetchError(own, TEST_ENDPOINT, 1000)).toBe(own);
});

Deno.test("classifyFetchError - 其余错误兜底归 network(保守分类)", () => {
  const n = classifyFetchError(new Error("something weird"), TEST_ENDPOINT, 1000);
  expect(n.kind).toBe("network");
  expect(n.fields.err).toContain("something weird");
});

// ---------------------------------------------------------------------------
// isRetryable:重试判定
// ---------------------------------------------------------------------------

Deno.test("isRetryable - timeout/network/5xx 为瞬态错,可重试", () => {
  expect(isRetryable({ kind: "timeout", fields: { endpoint: TEST_ENDPOINT } })).toBe(true);
  expect(isRetryable({ kind: "network", fields: { endpoint: TEST_ENDPOINT } })).toBe(true);
  expect(isRetryable({ kind: "http", fields: { endpoint: TEST_ENDPOINT, status: 500 } })).toBe(true);
  expect(isRetryable({ kind: "http", fields: { endpoint: TEST_ENDPOINT, status: 503 } })).toBe(true);
});

Deno.test("isRetryable - 4xx(含 408/429)与 config 为语义错,不可重试", () => {
  expect(isRetryable({ kind: "http", fields: { endpoint: TEST_ENDPOINT, status: 401 } })).toBe(false);
  expect(isRetryable({ kind: "http", fields: { endpoint: TEST_ENDPOINT, status: 408 } })).toBe(false);
  expect(isRetryable({ kind: "http", fields: { endpoint: TEST_ENDPOINT, status: 429 } })).toBe(false);
  expect(isRetryable({ kind: "config", fields: { endpoint: TEST_ENDPOINT } })).toBe(false);
});

// ---------------------------------------------------------------------------
// callProvider 重试流(集成:mock globalThis.fetch)
// 注意:AbortSignal.timeout 的定时器可能存活到信号超时之后,
// 这些用例关闭 op/resource 消毒器以容忍残留 timer(仅限本组用例)。
// ---------------------------------------------------------------------------

Deno.test("重试流 - 第一次 TimeoutError 第二次成功,translate 正常返回且 fetch 恰 2 次", { sanitizeOps: false, sanitizeResources: false }, async () => {
  setupLlmEnv();
  try {
    let calls = 0;
    globalThis.fetch = async (): Promise<Response> => {
      calls++;
      if (calls === 1) {
        throw new DOMException("The operation was aborted due to timeout", "TimeoutError");
      }
      return openaiOk('{"command":"uptime","args":[],"explanation":"Check uptime"}');
    };

    // 重试成功 = happy path:不产生任何 error 痕迹(决策 10),直接拿到内容
    const result = await translate("show uptime");
    expect(result).toBe('{"command":"uptime","args":[],"explanation":"Check uptime"}');
    expect(calls).toBe(2);
  } finally {
    teardownLlmEnv();
  }
});

Deno.test("重试流 - 第一次 TypeError(network) 第二次成功,fetch 恰 2 次", { sanitizeOps: false, sanitizeResources: false }, async () => {
  setupLlmEnv();
  try {
    let calls = 0;
    globalThis.fetch = async (): Promise<Response> => {
      calls++;
      if (calls === 1) {
        throw new TypeError("error trying to connect: dns error");
      }
      return openaiOk('{"command":"df","args":["-h"],"explanation":"disk"}');
    };

    const result = await translate("show disk usage");
    expect(result).toBe('{"command":"df","args":["-h"],"explanation":"disk"}');
    expect(calls).toBe(2);
  } finally {
    teardownLlmEnv();
  }
});

Deno.test("重试流 - HTTP 401 不重试:抛 kind=http/status=401,fetch 恰 1 次", { sanitizeOps: false, sanitizeResources: false }, async () => {
  setupLlmEnv();
  try {
    let calls = 0;
    globalThis.fetch = async (): Promise<Response> => {
      calls++;
      return new Response("invalid api key", { status: 401 });
    };

    let thrown: (Error & NormalizedLLMError) | null = null;
    try {
      await translate("show uptime");
    } catch (e) {
      thrown = e as Error & NormalizedLLMError;
    }
    expect(thrown).not.toBeNull();
    expect(thrown!.kind).toBe("http");
    expect(thrown!.fields.status).toBe(401);
    expect(thrown!.fields.endpoint).toBe(TEST_ENDPOINT);
    expect(thrown!.fields.body).toBe("invalid api key");
    expect(calls).toBe(1);
  } finally {
    teardownLlmEnv();
  }
});

Deno.test("重试流 - HTTP 500 重试一次:fetch 恰 2 次,最终错误 kind=http/status=500", { sanitizeOps: false, sanitizeResources: false }, async () => {
  setupLlmEnv();
  try {
    let calls = 0;
    globalThis.fetch = async (): Promise<Response> => {
      calls++;
      return new Response("server overloaded", { status: 500 });
    };

    let thrown: (Error & NormalizedLLMError) | null = null;
    try {
      await translate("show uptime");
    } catch (e) {
      thrown = e as Error & NormalizedLLMError;
    }
    expect(thrown).not.toBeNull();
    expect(thrown!.kind).toBe("http");
    expect(thrown!.fields.status).toBe(500);
    expect(calls).toBe(2);
  } finally {
    teardownLlmEnv();
  }
});

Deno.test("重试流 - 超时持续失败:fetch 恰 2 次(只重试 1 次),最终错误 kind=timeout 带 endpoint/timeoutMs", { sanitizeOps: false, sanitizeResources: false }, async () => {
  setupLlmEnv();
  try {
    let calls = 0;
    globalThis.fetch = async (): Promise<Response> => {
      calls++;
      throw new DOMException("The operation was aborted due to timeout", "TimeoutError");
    };

    let thrown: (Error & NormalizedLLMError) | null = null;
    try {
      await translate("show uptime");
    } catch (e) {
      thrown = e as Error & NormalizedLLMError;
    }
    expect(thrown).not.toBeNull();
    expect(thrown!.kind).toBe("timeout");
    expect(thrown!.fields.endpoint).toBe(TEST_ENDPOINT);
    expect(thrown!.fields.timeoutMs).toBe(1000);
    // 决策 5:最多 1 次重试 → 最多 2 次调用
    expect(calls).toBe(2);
  } finally {
    teardownLlmEnv();
  }
});

Deno.test("重试流 - 第二次失败以第二次的规范化错误为准(先 timeout 后 401 → kind=http/401)", { sanitizeOps: false, sanitizeResources: false }, async () => {
  setupLlmEnv();
  try {
    let calls = 0;
    globalThis.fetch = async (): Promise<Response> => {
      calls++;
      if (calls === 1) {
        throw new DOMException("The operation was aborted due to timeout", "TimeoutError");
      }
      return new Response("invalid api key", { status: 401 });
    };

    let thrown: (Error & NormalizedLLMError) | null = null;
    try {
      await translate("show uptime");
    } catch (e) {
      thrown = e as Error & NormalizedLLMError;
    }
    expect(thrown).not.toBeNull();
    // 最终抛的是第二次(更新更准)的错误,而不是第一次的 timeout
    expect(thrown!.kind).toBe("http");
    expect(thrown!.fields.status).toBe(401);
    expect(calls).toBe(2);
  } finally {
    teardownLlmEnv();
  }
});

Deno.test("重试流 - 响应 ok 但 content 非字符串:kind=config,不重试(fetch 恰 1 次)", { sanitizeOps: false, sanitizeResources: false }, async () => {
  setupLlmEnv();
  try {
    let calls = 0;
    globalThis.fetch = async (): Promise<Response> => {
      calls++;
      return new Response(
        JSON.stringify({ choices: [{ message: { content: 42 } }] }),
        { status: 200 },
      );
    };

    let thrown: (Error & NormalizedLLMError) | null = null;
    try {
      await translate("show uptime");
    } catch (e) {
      thrown = e as Error & NormalizedLLMError;
    }
    expect(thrown).not.toBeNull();
    expect(thrown!.kind).toBe("config");
    expect(thrown!.fields.endpoint).toBe(TEST_ENDPOINT);
    expect(typeof thrown!.fields.err).toBe("string");
    expect(calls).toBe(1);
  } finally {
    teardownLlmEnv();
  }
});

Deno.test("重试流 - 响应 ok 但 JSON 解析炸:kind=config,不重试(fetch 恰 1 次)", { sanitizeOps: false, sanitizeResources: false }, async () => {
  setupLlmEnv();
  try {
    let calls = 0;
    globalThis.fetch = async (): Promise<Response> => {
      calls++;
      return new Response("not-json-at-all{{{", { status: 200 });
    };

    let thrown: (Error & NormalizedLLMError) | null = null;
    try {
      await translate("show uptime");
    } catch (e) {
      thrown = e as Error & NormalizedLLMError;
    }
    expect(thrown).not.toBeNull();
    expect(thrown!.kind).toBe("config");
    expect(thrown!.fields.endpoint).toBe(TEST_ENDPOINT);
    expect(calls).toBe(1);
  } finally {
    teardownLlmEnv();
  }
});

Deno.test("重试流 - HTTP 错误体超长截断:fields.body 最多 200 字符", { sanitizeOps: false, sanitizeResources: false }, async () => {
  setupLlmEnv();
  try {
    const longBody = "e".repeat(500);
    let calls = 0;
    globalThis.fetch = async (): Promise<Response> => {
      calls++;
      return new Response(longBody, { status: 400 });
    };

    let thrown: (Error & NormalizedLLMError) | null = null;
    try {
      await translate("show uptime");
    } catch (e) {
      thrown = e as Error & NormalizedLLMError;
    }
    expect(thrown).not.toBeNull();
    expect(thrown!.kind).toBe("http");
    expect(thrown!.fields.status).toBe(400);
    expect(thrown!.fields.body).toBe("e".repeat(200));
    expect(calls).toBe(1);
  } finally {
    teardownLlmEnv();
  }
});
