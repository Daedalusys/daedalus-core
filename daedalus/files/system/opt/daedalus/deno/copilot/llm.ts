/**
 * Daedalus OS Copilot LLM 适配器。
 *
 * 通过兼容 OpenAI 和 Anthropic 的 API 提供非流式转换和多轮修订。
 * 强制执行严格的模式配置、30 秒获取超时，并在修订轮次中保留不可变的系统提示词。
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

/**
 * 对配置的 LLM 提供商执行补全请求。
 */
async function callProvider(
  messages: ChatMessage[],
  configOverrides?: Partial<Config>,
): Promise<string> {
  const config = readConfig(configOverrides);
  const timeoutMs = 30_000;
  const signal = AbortSignal.timeout(timeoutMs);

  if (config.provider === "openai") {
    let openaiMessages = messages;
    if (openaiMessages.length === 0 || openaiMessages[0].role !== "system") {
      openaiMessages = [
        { role: "system", content: buildSystemPrompt() },
        ...openaiMessages,
      ];
    }

    const endpoint = `${config.baseUrl.replace(/\/+$/, "")}/chat/completions`;
    const headers = {
      "Content-Type": "application/json",
      Authorization: `Bearer ${config.apiKey}`,
    };
    const body = JSON.stringify({
      model: config.model,
      messages: openaiMessages,
      response_format: { type: "json_object" },
    });

    const response = await fetch(endpoint, {
      method: "POST",
      headers,
      body,
      signal,
    });

    if (!response.ok) {
      const errText = await response.text();
      throw new Error(`OpenAI API error (${response.status}): ${errText}`);
    }

    const data = await response.json();
    const content = data?.choices?.[0]?.message?.content;
    if (typeof content !== "string") {
      throw new Error("OpenAI response missing message content");
    }

    return content;
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

    const endpoint = `${config.baseUrl.replace(/\/+$/, "")}/v1/messages`;
    const headers = {
      "Content-Type": "application/json",
      "x-api-key": config.apiKey,
      "anthropic-version": "2023-06-01",
    };
    const body = JSON.stringify({
      model: config.model,
      max_tokens: 1024,
      system: systemPrompt,
      messages: userAssistantMessages,
    });

    const response = await fetch(endpoint, {
      method: "POST",
      headers,
      body,
      signal,
    });

    if (!response.ok) {
      const errText = await response.text();
      throw new Error(`Anthropic API error (${response.status}): ${errText}`);
    }

    const data = await response.json();
    const content = data?.content?.[0]?.text;
    if (typeof content !== "string") {
      throw new Error("Anthropic response missing text content");
    }

    return content;
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
