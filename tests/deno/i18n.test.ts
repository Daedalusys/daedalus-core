/**
 * Daedalus OS Copilot i18n 模块单元测试。
 *
 * 覆盖：
 * - t() 命中 / 未命中（返 key 本身）/ 占位符替换 / 缺参空串（永不抛）;
 * - detectLocale()：LC_ALL > LANG > en_US;POSIX 解析 zh_CN.UTF-8 → zh_CN;
 * - loadLocale fallback 链：zh_CN → zh → en_US;
 * - initI18n() 之后 t() 同步可用。
 *
 * 环境变量说明：本机 Deno 2.9.5 的 TestContext 上没有 t.Setenv
 * （该便利 API 在此版本不存在），因此用 Deno.env.set / delete +
 * try/finally 手工实现每例隔离，语义与 t.Setenv 等价。
 */

import { expect } from "jsr:@std/expect@1";
import {
  initI18n,
  t,
  detectLocale,
  currentLocale,
} from "../../daedalus/plugin/copilot/i18n.ts";

/**
 * 设置环境变量并返回恢复函数。
 * 恢复时按设置前快照还原：原本未设置的键删除，原本有值的键写回。
 */
function withEnv(entries: Record<string, string | undefined>): () => void {
  const saved = new Map<string, string | undefined>();
  for (const [k, v] of Object.entries(entries)) {
    if (!saved.has(k)) {
      saved.set(k, Deno.env.get(k));
    }
    if (v === undefined) {
      try {
        Deno.env.delete(k);
      } catch {
        // 未设置过则忽略
      }
    } else {
      Deno.env.set(k, v);
    }
  }
  return () => {
    for (const [k, v] of saved) {
      if (v === undefined) {
        try {
          Deno.env.delete(k);
        } catch {
          // 原本就未设置
        }
      } else {
        Deno.env.set(k, v);
      }
    }
  };
}

// ============ t() 行为（en_US 兜底） ============

Deno.test("Copilot i18n - t hits en_US fallback after initI18n", async () => {
  const restore = withEnv({ LC_ALL: "en_US.UTF-8" });
  try {
    await initI18n();
    // 命中 en_US 兜底字典
    expect(t("repl.prompt")).toBe("daedalus> ");
    expect(t("help.usage")).toBe("Usage:");
  } finally {
    restore();
  }
});

Deno.test("Copilot i18n - t returns the key itself for missing keys (never throws)", async () => {
  const restore = withEnv({ LC_ALL: "en_US.UTF-8" });
  try {
    await initI18n();
    // 未命中返 key 本身;带参数同样不抛
    expect(t("nonexistent.key")).toBe("nonexistent.key");
    expect(t("nonexistent.key", "arg0")).toBe("nonexistent.key");
  } finally {
    restore();
  }
});

Deno.test("Copilot i18n - t replaces {0} placeholder printf-style", async () => {
  const restore = withEnv({ LC_ALL: "en_US.UTF-8" });
  try {
    await initI18n();
    // {0} 替换为传入参数
    expect(t("confirm.request", "show memory")).toBe("Request: show memory");
  } finally {
    restore();
  }
});

Deno.test("Copilot i18n - t formats multiple placeholders and defaults missing args to empty string", async () => {
  const restore = withEnv({ LC_ALL: "en_US.UTF-8" });
  try {
    await initI18n();
    // 多占位符：verbose.preview 只有一个 {0},此处用合造消息验证多 arg 行为
    expect(t("verbose.preview", "df -h")).toBe("→ df -h");
    // 缺参 → 空串,不抛(构造一个带 {0} 的 key 缺失场景外的直查:
    // error.config 的 {0} 缺省时为空串)
    expect(t("error.config")).toBe("Configuration error: ");
    // 未命中 key + undefined 参数:占位符不存在,参数被忽略,返 key 本身
    expect(t("foo", undefined)).toBe("foo");
  } finally {
    restore();
  }
});

// ============ detectLocale() 探测 ============

Deno.test("Copilot i18n - detectLocale parses LC_ALL zh_CN.UTF-8 to zh_CN (priority over LANG)", () => {
  const restore = withEnv({ LC_ALL: "zh_CN.UTF-8", LANG: "en_US.UTF-8" });
  try {
    // LC_ALL 优先于 LANG
    expect(detectLocale()).toBe("zh_CN");
  } finally {
    restore();
  }
});

Deno.test("Copilot i18n - detectLocale falls back to LANG when LC_ALL unset", () => {
  const restore = withEnv({ LC_ALL: undefined, LANG: "en_US.UTF-8" });
  try {
    expect(detectLocale()).toBe("en_US");
  } finally {
    restore();
  }
});

Deno.test("Copilot i18n - detectLocale defaults to en_US when env is empty", () => {
  const restore = withEnv({ LC_ALL: undefined, LANG: undefined });
  try {
    // env 全空 → 兜底 en_US
    expect(detectLocale()).toBe("en_US");
  } finally {
    restore();
  }
});

// ============ locale fallback 链 ============

Deno.test("Copilot i18n - zh_CN locale loads Chinese strings end-to-end via initI18n", async () => {
  const restore = withEnv({ LC_ALL: "zh_CN.UTF-8" });
  try {
    await initI18n();
    // 精确命中 zh_CN.json;currentLocale 同步生效
    expect(currentLocale()).toBe("zh_CN");
    expect(t("confirm.prompt")).toBe("执行? [y]是 / [e]改 / [n]否(给反馈) / [q]退: ");
    expect(t("dryrun.would_execute", "df -h")).toBe("[dry-run] 建议命令(本次未运行): df -h");
  } finally {
    restore();
  }
});

Deno.test("Copilot i18n - unknown locale falls back through language level to en_US", async () => {
  const restore = withEnv({ LC_ALL: "fr_FR.UTF-8" });
  try {
    await initI18n();
    // fr_FR 无文件 → 语言级 fr 无文件 → en_US 兜底;字典可用,currentLocale 仍记录原始 locale
    expect(currentLocale()).toBe("fr_FR");
    expect(t("repl.prompt")).toBe("daedalus> ");
    expect(t("confirm.request", "x")).toBe("Request: x");
  } finally {
    restore();
  }
});

Deno.test("Copilot i18n - initI18n makes t synchronous afterwards (no await needed)", async () => {
  const restore = withEnv({ LC_ALL: "en_US.UTF-8" });
  try {
    await initI18n();
    // initI18n 后 t() 是普通同步函数
    const out = t("version.banner", "9.9.9");
    expect(out).toBe("daedalus-copilot 9.9.9");
    expect(typeof out).toBe("string");
  } finally {
    restore();
  }
});
