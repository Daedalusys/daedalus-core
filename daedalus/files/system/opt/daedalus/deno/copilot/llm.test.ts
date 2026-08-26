import { expect } from "jsr:@std/expect@1";
import { join } from "jsr:@std/path@1";
import { readConfig, translate, revise } from "./llm.ts";

let tempDir: string;
// 隔离配置文件路径：指向临时目录下【不存在】的文件。
// llm.ts 的 resolveConfigFilePath() 在 DAEDALUS_CONFIG_PATH 未设置时会回退到
// 真实 HOME 下的 ~/.config/daedalus/copilot.json，因此仅 delete 该环境变量
// 并不等于隔离——开发机上的个人配置会污染断言。显式指向不存在的路径后，
// resolveConfigFilePath() 优先返回该路径，tryReadConfigFile() 静默返回 null，
// HOME 根本不会被读取，无需再对 HOME 做双隔离。
let isolatedConfigPath: string;
const originalFetch = globalThis.fetch;

function setup() {
  Deno.env.delete("DAEDALUS_LLM_PROVIDER");
  Deno.env.delete("DAEDALUS_LLM_API_KEY");
  Deno.env.delete("DAEDALUS_LLM_MODEL");
  Deno.env.delete("DAEDALUS_LLM_BASE_URL");

  tempDir = Deno.makeTempDirSync({ prefix: "daedalus-llm-test-" });

  // 关键隔离步骤：不再 delete DAEDALUS_CONFIG_PATH，而是显式指向不存在的临时路径，
  // 保证「无配置文件」语义与宿主机个人配置无关（hermetic）。
  isolatedConfigPath = join(tempDir, "isolated", "copilot.json");
  Deno.env.set("DAEDALUS_CONFIG_PATH", isolatedConfigPath);
}

function teardown() {
  globalThis.fetch = originalFetch;
  Deno.env.delete("DAEDALUS_LLM_PROVIDER");
  Deno.env.delete("DAEDALUS_LLM_API_KEY");
  Deno.env.delete("DAEDALUS_LLM_MODEL");
  Deno.env.delete("DAEDALUS_LLM_BASE_URL");
  Deno.env.delete("DAEDALUS_CONFIG_PATH");

  try {
    Deno.removeSync(tempDir, { recursive: true });
  } catch {
    // 忽略清理错误
  }
}

Deno.test("Copilot LLM - readConfig throws error when apiKey is missing from overrides, env, and config file", () => {
  setup();
  try {
    // hermetic 前提检查：隔离配置路径必须不存在，
    // 否则会读到宿主机真实配置导致本断言环境耦合失败
    expect(() => Deno.statSync(isolatedConfigPath)).toThrow();
    expect(() => readConfig()).toThrow(
      "missing LLM API key. Set DAEDALUS_LLM_API_KEY or configure ~/.config/daedalus/copilot.json",
    );
  } finally {
    teardown();
  }
});

Deno.test("Copilot LLM - readConfig uses default values for openai when only apiKey is provided", () => {
  setup();
  try {
    const config = readConfig({ apiKey: "sk-test-123" });
    expect(config).toEqual({
      provider: "openai",
      apiKey: "sk-test-123",
      model: "gpt-4o-mini",
      baseUrl: "https://api.openai.com/v1",
    });
  } finally {
    teardown();
  }
});

Deno.test("Copilot LLM - readConfig uses default values for anthropic when provider is anthropic", () => {
  setup();
  try {
    const config = readConfig({
      provider: "anthropic",
      apiKey: "sk-ant-test-123",
    });
    expect(config).toEqual({
      provider: "anthropic",
      apiKey: "sk-ant-test-123",
      model: "claude-3-5-haiku-latest",
      baseUrl: "https://api.anthropic.com",
    });
  } finally {
    teardown();
  }
});

Deno.test("Copilot LLM - readConfig reads configuration from environment variables", () => {
  setup();
  try {
    Deno.env.set("DAEDALUS_LLM_PROVIDER", "anthropic");
    Deno.env.set("DAEDALUS_LLM_API_KEY", "env-ant-key");
    Deno.env.set("DAEDALUS_LLM_MODEL", "claude-custom");
    Deno.env.set("DAEDALUS_LLM_BASE_URL", "https://custom.anthropic.internal");

    const config = readConfig();
    expect(config).toEqual({
      provider: "anthropic",
      apiKey: "env-ant-key",
      model: "claude-custom",
      baseUrl: "https://custom.anthropic.internal",
    });
  } finally {
    teardown();
  }
});

Deno.test("Copilot LLM - readConfig reads configuration from config file specified in DAEDALUS_CONFIG_PATH", () => {
  setup();
  try {
    const configPath = `${tempDir}/copilot.json`;
    Deno.writeTextFileSync(
      configPath,
      JSON.stringify({
        provider: "openai",
        api_key: "file-openai-key",
        model: "gpt-4o-custom",
        base_url: "https://custom.openai.internal/v1",
      }),
    );

    Deno.env.set("DAEDALUS_CONFIG_PATH", configPath);

    const config = readConfig();
    expect(config).toEqual({
      provider: "openai",
      apiKey: "file-openai-key",
      model: "gpt-4o-custom",
      baseUrl: "https://custom.openai.internal/v1",
    });
  } finally {
    teardown();
  }
});

Deno.test("Copilot LLM - readConfig enforces precedence: overrides > env vars > config file", () => {
  setup();
  try {
    const configPath = `${tempDir}/copilot.json`;
    Deno.writeTextFileSync(
      configPath,
      JSON.stringify({
        provider: "openai",
        api_key: "file-key",
        model: "file-model",
        base_url: "https://file-url",
      }),
    );
    Deno.env.set("DAEDALUS_CONFIG_PATH", configPath);

    // 环境变量覆盖配置文件
    Deno.env.set("DAEDALUS_LLM_API_KEY", "env-key");
    Deno.env.set("DAEDALUS_LLM_MODEL", "env-model");

    // 显式覆盖优先于环境变量和配置文件
    const config = readConfig({
      model: "override-model",
    });

    expect(config.apiKey).toBe("env-key");
    expect(config.model).toBe("override-model");
    expect(config.baseUrl).toBe("https://file-url");
    expect(config.provider).toBe("openai");
  } finally {
    teardown();
  }
});

Deno.test("Copilot LLM - translate sends correct JSON-formatted payload and headers to OpenAI", async () => {
  setup();
  try {
    let capturedUrl = "";
    let capturedHeaders: Record<string, string> = {};
    let capturedBody: any = null;
    let capturedSignal: any = null;

    globalThis.fetch = async (url: any, init: any) => {
      capturedUrl = url.toString();
      capturedHeaders = init.headers;
      capturedBody = JSON.parse(init.body);
      capturedSignal = init.signal;

      return new Response(
        JSON.stringify({
          choices: [
            {
              message: {
                content:
                  '{"command":"df","args":["-h"],"explanation":"Check disk space"}',
              },
            },
          ],
        }),
        {
          status: 200,
          headers: { "Content-Type": "application/json" },
        },
      );
    };

    const result = await translate("show disk usage", {
      apiKey: "test-openai-key",
      baseUrl: "https://api.openai.com/v1/",
    });

    expect(result).toBe(
      '{"command":"df","args":["-h"],"explanation":"Check disk space"}',
    );
    expect(capturedUrl).toBe("https://api.openai.com/v1/chat/completions");
    expect(capturedHeaders["Authorization"]).toBe("Bearer test-openai-key");
    expect(capturedHeaders["Content-Type"]).toBe("application/json");
    expect(capturedBody.model).toBe("gpt-4o-mini");
    expect(capturedBody.response_format).toEqual({ type: "json_object" });
    expect(Array.isArray(capturedBody.messages)).toBe(true);
    expect(capturedBody.messages[0].role).toBe("system");
    expect(capturedBody.messages[0].content).toContain("ALLOW_COMMANDS");
    expect(capturedBody.messages[1].role).toBe("user");
    expect(capturedBody.messages[1].content).toBe("show disk usage");
    expect(capturedSignal).toBeDefined();
  } finally {
    teardown();
  }
});

Deno.test("Copilot LLM - translate sends correct payload and headers to Anthropic", async () => {
  setup();
  try {
    let capturedUrl = "";
    let capturedHeaders: Record<string, string> = {};
    let capturedBody: any = null;
    let capturedSignal: any = null;

    globalThis.fetch = async (url: any, init: any) => {
      capturedUrl = url.toString();
      capturedHeaders = init.headers;
      capturedBody = JSON.parse(init.body);
      capturedSignal = init.signal;

      return new Response(
        JSON.stringify({
          content: [
            {
              text: '{"command":"uptime","args":[],"explanation":"Check uptime"}',
            },
          ],
        }),
        {
          status: 200,
          headers: { "Content-Type": "application/json" },
        },
      );
    };

    const result = await translate("how long has system been running", {
      provider: "anthropic",
      apiKey: "test-ant-key",
      baseUrl: "https://api.anthropic.com",
    });

    expect(result).toBe(
      '{"command":"uptime","args":[],"explanation":"Check uptime"}',
    );
    expect(capturedUrl).toBe("https://api.anthropic.com/v1/messages");
    expect(capturedHeaders["x-api-key"]).toBe("test-ant-key");
    expect(capturedHeaders["anthropic-version"]).toBe("2023-06-01");
    expect(capturedHeaders["Content-Type"]).toBe("application/json");
    expect(capturedBody.model).toBe("claude-3-5-haiku-latest");
    expect(capturedBody.max_tokens).toBe(1024);
    expect(typeof capturedBody.system).toBe("string");
    expect(capturedBody.system).toContain("ALLOW_COMMANDS");
    expect(Array.isArray(capturedBody.messages)).toBe(true);
    expect(capturedBody.messages.length).toBe(1);
    expect(capturedBody.messages[0].role).toBe("user");
    expect(capturedBody.messages[0].content).toBe(
      "how long has system been running",
    );
    expect(capturedSignal).toBeDefined();
  } finally {
    teardown();
  }
});

Deno.test("Copilot LLM - translate propagates API errors from HTTP failure", async () => {
  setup();
  try {
    globalThis.fetch = async () => {
      return new Response("Quota exceeded", { status: 429 });
    };

    let thrownError: Error | null = null;
    try {
      await translate("test", { apiKey: "test-key" });
    } catch (e) {
      thrownError = e as Error;
    }
    expect(thrownError).not.toBeNull();
    expect(thrownError?.message).toContain("OpenAI API error (429): Quota exceeded");
  } finally {
    teardown();
  }
});

Deno.test("Copilot LLM - revise preserves system prompt and appends user feedback to message history", async () => {
  setup();
  try {
    let capturedBody: any = null;

    globalThis.fetch = async (_url: any, init: any) => {
      capturedBody = JSON.parse(init.body);
      return new Response(
        JSON.stringify({
          choices: [
            {
              message: {
                content:
                  '{"command":"df","args":["-h","/tmp"],"explanation":"Check /tmp disk usage"}',
              },
            },
          ],
        }),
        { status: 200 },
      );
    };

    const systemPrompt = "INVARIANT_SYSTEM_PROMPT";
    const history: Array<{
      role: "system" | "user" | "assistant";
      content: string;
    }> = [
      { role: "system", content: systemPrompt },
      { role: "user", content: "show disk usage" },
      {
        role: "assistant",
        content: '{"command":"df","args":["-h"],"explanation":"Check disk"}',
      },
    ];

    const result = await revise(history, "only check /tmp directory", {
      apiKey: "test-key",
    });

    expect(result).toBe(
      '{"command":"df","args":["-h","/tmp"],"explanation":"Check /tmp disk usage"}',
    );
    expect(capturedBody.messages.length).toBe(4);
    expect(capturedBody.messages[0]).toEqual({
      role: "system",
      content: systemPrompt,
    });
    expect(capturedBody.messages[1]).toEqual({
      role: "user",
      content: "show disk usage",
    });
    expect(capturedBody.messages[2]).toEqual({
      role: "assistant",
      content: '{"command":"df","args":["-h"],"explanation":"Check disk"}',
    });
    expect(capturedBody.messages[3]).toEqual({
      role: "user",
      content: "only check /tmp directory",
    });
  } finally {
    teardown();
  }
});

Deno.test("Copilot LLM - revise handles Anthropic revise with system prompt in system field and user feedback appended", async () => {
  setup();
  try {
    let capturedBody: any = null;

    globalThis.fetch = async (_url: any, init: any) => {
      capturedBody = JSON.parse(init.body);
      return new Response(
        JSON.stringify({
          content: [
            {
              text: '{"command":"free","args":["-m"],"explanation":"Check memory in MB"}',
            },
          ],
        }),
        { status: 200 },
      );
    };

    const systemPrompt = "ANTHROPIC_SYSTEM_PROMPT";
    const history: Array<{
      role: "system" | "user" | "assistant";
      content: string;
    }> = [
      { role: "system", content: systemPrompt },
      { role: "user", content: "show memory" },
      {
        role: "assistant",
        content: '{"command":"free","args":["-h"],"explanation":"memory"}',
      },
    ];

    const result = await revise(history, "show in MB instead", {
      provider: "anthropic",
      apiKey: "ant-key",
    });

    expect(result).toBe(
      '{"command":"free","args":["-m"],"explanation":"Check memory in MB"}',
    );
    expect(capturedBody.system).toBe(systemPrompt);
    expect(capturedBody.messages.length).toBe(3);
    expect(capturedBody.messages[0]).toEqual({
      role: "user",
      content: "show memory",
    });
    expect(capturedBody.messages[1]).toEqual({
      role: "assistant",
      content: '{"command":"free","args":["-h"],"explanation":"memory"}',
    });
    expect(capturedBody.messages[2]).toEqual({
      role: "user",
      content: "show in MB instead",
    });
  } finally {
    teardown();
  }
});

Deno.test("Copilot LLM - Timeout & Fetch Error Handling propagates network and timeout errors without hanging", async () => {
  setup();
  try {
    globalThis.fetch = async () => {
      const err = new Error("The operation was aborted due to timeout");
      err.name = "TimeoutError";
      throw err;
    };

    let thrownError: Error | null = null;
    try {
      await translate("show uptime", { apiKey: "test-key" });
    } catch (e) {
      thrownError = e as Error;
    }
    expect(thrownError).not.toBeNull();
    expect(thrownError?.message).toContain("The operation was aborted due to timeout");
  } finally {
    teardown();
  }
});
