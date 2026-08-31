/**
 * Daedalus OS Copilot LLM 适配器。
 *
 * 通过兼容 OpenAI 和 Anthropic 的 API 提供非流式转换和多轮修订。
 * 强制执行严格的模式配置;超时可配置(env DAEDALUS_LLM_TIMEOUT_MS,
 * 默认 30s,clamp [1s, 5min]),失败时统一分类为 timeout/http/network/config
 * 四类并以 Error + kind/fields 附加属性抛出;瞬态错(timeout/network/5xx)
 * 在 callProvider 内部透明重试 1 次(固定 2s 退避),4xx 与 config 直接抛。
 * 修订轮次中保留不可变的系统提示词。
 */

import { buildSystemPrompt } from "./policy.ts";

export interface Config {
  provider: "openai" | "anthropic";
  apiKey: string;
  model: string;
  baseUrl: string;
}

export interface ChatMessage {
  role: "system" | "user" | "assistant";
  content: string;
}

/**
 * 在 Deno 和 Node/Bun 环境中安全获取环境变量。
 */
function getEnv(key: string): string | undefined {
  if (typeof (globalThis as any).Deno?.env?.get === "function") {
    return (globalThis as any).Deno.env.get(key);
  }
  return process.env?.[key];
}

/**
 * 解析 ~/.config/daedalus/copilot.json 的路径或 DAEDALUS_CONFIG_PATH 覆盖路径。
 */
function resolveConfigFilePath(): string {
  const envPath = getEnv("DAEDALUS_CONFIG_PATH");
  if (envPath) {
    return envPath;
  }
  const home = getEnv("HOME") || "/root";
  return `${home}/.config/daedalus/copilot.json`;
}

/**
 * 尝试读取并解析 JSON 配置文件。
 * 若文件不存在、无法读取或 JSON 无效，则静默返回 null。
 */
function tryReadConfigFile(configPath: string): Record<string, unknown> | null {
  try {
    let content: string | null = null;
    if (typeof (globalThis as any).Deno?.readTextFileSync === "function") {
      content = (globalThis as any).Deno.readTextFileSync(configPath);
    } else {
      try {
        const fs = require("fs");
        if (fs.existsSync(configPath)) {
          content = fs.readFileSync(configPath, "utf-8");
        }
      } catch {
        // 忽略回退读取失败
      }
    }

    if (content && typeof content === "string" && content.trim().length > 0) {
      const parsed = JSON.parse(content);
      if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
        return parsed as Record<string, unknown>;
      }
    }
  } catch {
    // 静默忽略缺失或无法读取的配置文件
  }
  return null;
}

/**
 * 读取并解析 LLM 配置。
 *
 * 优先级：
 * 1. 显式覆盖对象 overrides
 * 2. 环境变量（DAEDALUS_LLM_API_KEY, DAEDALUS_LLM_PROVIDER, DAEDALUS_LLM_MODEL, DAEDALUS_LLM_BASE_URL）
 * 3. 配置文件（~/.config/daedalus/copilot.json 或 DAEDALUS_CONFIG_PATH）
 *
 * 默认值：
 * - Provider: "openai"
 * - OpenAI: model 为 "gpt-4o-mini", baseUrl 为 "https://api.openai.com/v1"
 * - Anthropic: model 为 "claude-3-5-haiku-latest", baseUrl 为 "https://api.anthropic.com"
 *
 * 若解析出的 apiKey 为空则抛出异常。
 */
export function readConfig(overrides?: {
  provider?: string;
  apiKey?: string;
  model?: string;
  baseUrl?: string;
}): Config {
  const configPath = resolveConfigFilePath();
  const fileConfig = tryReadConfigFile(configPath) || {};

  const fileProvider =
    typeof fileConfig.provider === "string" ? fileConfig.provider : undefined;
  const fileApiKey =
    typeof fileConfig.api_key === "string"
      ? fileConfig.api_key
      : typeof fileConfig.apiKey === "string"
      ? fileConfig.apiKey
      : undefined;
  const fileModel =
    typeof fileConfig.model === "string" ? fileConfig.model : undefined;
  const fileBaseUrl =
    typeof fileConfig.base_url === "string"
      ? fileConfig.base_url
      : typeof fileConfig.baseUrl === "string"
      ? fileConfig.baseUrl
      : undefined;

  const envProvider = getEnv("DAEDALUS_LLM_PROVIDER");
  const envApiKey = getEnv("DAEDALUS_LLM_API_KEY");
  const envModel = getEnv("DAEDALUS_LLM_MODEL");
  const envBaseUrl = getEnv("DAEDALUS_LLM_BASE_URL");

  const rawProvider = (
    overrides?.provider ||
    envProvider ||
    fileProvider ||
    "openai"
  )
    .trim()
    .toLowerCase();

  const provider: "openai" | "anthropic" =
    rawProvider === "anthropic" ? "anthropic" : "openai";

  const apiKey = (
    overrides?.apiKey ??
    envApiKey ??
    fileApiKey ??
    ""
  ).trim();

  if (!apiKey) {
    throw new Error(
      "missing LLM API key. Set DAEDALUS_LLM_API_KEY or configure ~/.config/daedalus/copilot.json",
    );
  }

  const defaultModel =
    provider === "anthropic" ? "claude-3-5-haiku-latest" : "gpt-4o-mini";
  const defaultBaseUrl =
    provider === "anthropic"
      ? "https://api.anthropic.com"
      : "https://api.openai.com/v1";

  const model = (
    overrides?.model ||
    envModel ||
    fileModel ||
    defaultModel
  ).trim();

  const baseUrl = (
    overrides?.baseUrl ||
    envBaseUrl ||
    fileBaseUrl ||
    defaultBaseUrl
  ).trim();

  return {
    provider,
    apiKey,
    model,
    baseUrl,
  };
}

// ---------------------------------------------------------------------------
// advisor-robustness:错误规范化与可配置超时(plan 3.1-3.3)
// ---------------------------------------------------------------------------

/** 错误分类:4 种覆盖所有失败模式(决策 3) */
export type ErrorKind = "timeout" | "http" | "network" | "config";

/** 规范化错误形状;throw 时以普通属性附加到 Error 上(决策 7:不引入子类) */
export interface NormalizedLLMError {
  kind: ErrorKind;
  fields: {
    /** 总是提供:实际请求的端点 URL */
    endpoint: string;
    /** timeout 时:本次生效的超时毫秒数 */
    timeoutMs?: number;
    /** http 时:HTTP 响应状态码 */
    status?: number;
    /** http 时:响应体(截断 200 字符) */
    body?: string;
    /** network/config 时:原始错误信息 */
    err?: string;
  };
}

/** 默认超时毫秒数:env 未设/非法时的兜底(决策 2) */
const DEFAULT_TIMEOUT_MS = 30_000;
/** 唯一一次重试的固定退避毫秒数(决策 5:1 次重试 + 2s,无指数退避) */
const RETRY_DELAY_MS = 2_000;

/**
 * 重试退避的测试注入口:内部重试读取 `.ms` 作为真实等待时长。
 * 测试置 0 免等真 2s,生产缺省 2000 不动。
 * 必须是可变对象属性——ESM 导入绑定不允许 importer 重新赋值。
 */
export const _retryDelayMsForTest: { ms: number } = { ms: RETRY_DELAY_MS };

/**
 * 解析 LLM 请求超时(毫秒)。
 *
 * 读 env DAEDALUS_LLM_TIMEOUT_MS(决策 1,与 exec.ts 的
 * DAEDALUS_WATCHDOG_TIMEOUT_MS 同款命名);
 * 未设/非数字/空白/非正一律回退默认 30000,合法值 clamp 到 [1000, 300000]
 * (决策 2:防 0 立即 abort、防超大值永久挂起)。
 */
export function parseTimeoutMs(): number {
  const raw = getEnv("DAEDALUS_LLM_TIMEOUT_MS");
  if (!raw) return DEFAULT_TIMEOUT_MS;
  const n = Number.parseInt(raw, 10);
  if (!Number.isFinite(n) || n <= 0) return DEFAULT_TIMEOUT_MS;
  return Math.min(300_000, Math.max(1_000, n));
}

/**
 * 把任意 fetch 层错误归一化为 NormalizedLLMError(plan 3.3)。
 *
 * - 自带 kind 属性(我们 attempt() 自抛的 http/config 结构化错误)→ 原样返回
 * - name === "TimeoutError"(含 AbortSignal.timeout 抛的 DOMException)→ timeout
 * - TypeError(fetch 网络层:DNS/拒绝连接/断连)→ network(带 err.message)
 * - 其余 → network(兜底,保守分类)
 */
export function classifyFetchError(
  err: unknown,
  endpoint: string,
  timeoutMs: number,
): NormalizedLLMError {
  if (err && typeof err === "object" && "kind" in (err as Record<string, unknown>)) {
    return err as NormalizedLLMError;
  }
  if (
    (err instanceof Error ||
      (typeof DOMException !== "undefined" && err instanceof DOMException)) &&
    err.name === "TimeoutError"
  ) {
    return { kind: "timeout", fields: { endpoint, timeoutMs } };
  }
  if (err instanceof TypeError) {
    return {
      kind: "network",
      fields: { endpoint, timeoutMs, err: err.message },
    };
  }
  return {
    kind: "network",
    fields: { endpoint, timeoutMs, err: String(err) },
  };
}

/**
 * 是否值得重试(决策 4)。
 * timeout/network/5xx 是典型瞬态错 → 重试大概率成功;
 * 4xx(含 408/429)与 config 是语义错(请求构造/权限/限流)→ 重试无意义。
 */
export function isRetryable(n: NormalizedLLMError): boolean {
  if (n.kind === "timeout" || n.kind === "network") return true;
  if (n.kind === "http" && (n.fields.status ?? 0) >= 500) return true;
  return false;
}

/**
 * 把规范化后的错误形状落回一个可 throw 的 Error(决策 7:Error + 附加属性)。
 * 若原错误已是自抛的结构化 Error(http/config)→ 原样返回;
 * 否则保留原始 message(timeout/network 分类场景),再附加 kind/fields。
 */
function toThrowError(err: unknown, norm: NormalizedLLMError): Error {
  if (err instanceof Error && "kind" in err) {
    return err;
  }
  const raw = err as { message?: unknown } | null;
  const message =
    raw !== null && typeof raw === "object" && typeof raw.message === "string" &&
      raw.message.length > 0
      ? raw.message
      : String(err);
  return Object.assign(new Error(message), norm);
}

/**
 * 对配置的 LLM 提供商执行补全请求。
 *
 * 内部透明重试(决策 6):attempt() 每次新建 AbortSignal(timeout 信号不可复用,
 * 复用旧信号会让第二次 fetch 立即 abort);第一次失败 → classify →
 * 不可重试直接抛结构化错误;可重试则固定退避后第二次 attempt;
 * 第二次仍失败以第二次的规范化错误抛出(更新更准);
 * 重试成功则正常返回,不留任何 error 痕迹(决策 10)。
 */
async function callProvider(
  messages: ChatMessage[],
  configOverrides?: Partial<Config>,
): Promise<string> {
  const config = readConfig(configOverrides);
  // 超时不再硬编码:env > 默认 30000,clamp [1000, 300000]
  const timeoutMs = parseTimeoutMs();

  const baseUrlTrimmed = config.baseUrl.replace(/\/+$/, "");
  const endpoint = config.provider === "openai"
    ? `${baseUrlTrimmed}/chat/completions`
    : `${baseUrlTrimmed}/v1/messages`;

  // 请求构造按 provider 分支;fetch/错误处理两侧共用同一 attempt() 逻辑
  let headers: Record<string, string>;
  let body: string;
  if (config.provider === "openai") {
    let openaiMessages = messages;
    if (openaiMessages.length === 0 || openaiMessages[0].role !== "system") {
      openaiMessages = [
        { role: "system", content: buildSystemPrompt() },
        ...openaiMessages,
      ];
    }

    headers = {
      "Content-Type": "application/json",
      Authorization: `Bearer ${config.apiKey}`,
    };
    body = JSON.stringify({
      model: config.model,
      messages: openaiMessages,
      response_format: { type: "json_object" },
    });
  } else {
    // Anthropic 适配器
    const systemMsg = messages.find((m) => m.role === "system");
    const systemPrompt = systemMsg ? systemMsg.content : buildSystemPrompt();
    const userAssistantMessages = messages
      .filter((m) => m.role !== "system")
      .map((m) => ({
        role: m.role as "user" | "assistant",
        content: m.content,
      }));

    headers = {
      "Content-Type": "application/json",
      "x-api-key": config.apiKey,
      "anthropic-version": "2023-06-01",
    };
    body = JSON.stringify({
      model: config.model,
      max_tokens: 1024,
      system: systemPrompt,
      messages: userAssistantMessages,
    });
  }

  /** 单次补全尝试:每次调用新建超时信号,失败以结构化 Error 抛出 */
  const attempt = async (): Promise<string> => {
    const signal = AbortSignal.timeout(timeoutMs);
    const response = await fetch(endpoint, {
      method: "POST",
      headers,
      body,
      signal,
    });

    if (!response.ok) {
      const errText = await response.text();
      // message 保留既有 "XX API error (status): body" 格式(llm.test.ts 特征化钉住),
      // 结构化信息走 kind/fields 附加属性供 main.ts(Wave 2)做 i18n 与审计
      throw Object.assign(
        new Error(
          `${config.provider === "openai" ? "OpenAI" : "Anthropic"} API error (${response.status}): ${errText}`,
        ),
        {
          kind: "http" as const,
          fields: {
            endpoint,
            status: response.status,
            body: errText.slice(0, 200),
            timeoutMs,
          },
        },
      );
    }

    // 响应 ok 但 JSON 解析失败 → config(Provider 返回体不是合法 JSON,重试无意义)
    let data: any;
    try {
      data = await response.json();
    } catch (err) {
      const reason = err instanceof Error ? err.message : String(err);
      throw Object.assign(
        new Error(`${config.provider} response is not valid JSON: ${reason}`),
        { kind: "config" as const, fields: { endpoint, err: reason } },
      );
    }

    const content = config.provider === "openai"
      ? data?.choices?.[0]?.message?.content
      : data?.content?.[0]?.text;
    if (typeof content !== "string") {
      // 响应 ok 但 content 提取失败 → config(响应 schema 不符合 Provider 约定)
      const reason = config.provider === "openai"
        ? "OpenAI response missing message content"
        : "Anthropic response missing text content";
      throw Object.assign(
        new Error(reason),
        { kind: "config" as const, fields: { endpoint, err: reason } },
      );
    }

    return content;
  };

  let firstError: unknown;
  try {
    return await attempt();
  } catch (err) {
    firstError = err;
  }

  const norm = classifyFetchError(firstError, endpoint, timeoutMs);
  if (!isRetryable(norm)) {
    throw toThrowError(firstError, norm);
  }

  await new Promise((resolve) => setTimeout(resolve, _retryDelayMsForTest.ms));
  try {
    return await attempt();
  } catch (err) {
    // 最终失败:以第二次的错误为准(更新更准)
    throw toThrowError(err, classifyFetchError(err, endpoint, timeoutMs));
  }
}

/**
 * 将自然语言查询转换为原始 LLM 响应。
 * 单轮非流式请求。
 */
export async function translate(
  query: string,
  configOverrides?: Partial<Config>,
): Promise<string> {
  const systemPrompt = buildSystemPrompt();
  const messages: ChatMessage[] = [
    { role: "system", content: systemPrompt },
    { role: "user", content: query },
  ];
  return await callProvider(messages, configOverrides);
}

/**
 * 根据用户反馈修订提议的命令。
 * 保留不变的系统提示词并追加用户反馈。
 */
export async function revise(
  history: Array<{ role: "system" | "user" | "assistant"; content: string }>,
  feedback: string,
  configOverrides?: Partial<Config>,
): Promise<string> {
  const updatedHistory: ChatMessage[] = [
    ...history,
    { role: "user", content: feedback },
  ];
  return await callProvider(updatedHistory, configOverrides);
}
