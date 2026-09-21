/**
 * Daedalus OS Copilot 插件的 i18n 模块。
 *
 * 本文件是 copilot 插件的 i18n 模块：t(key, ...args) 返回 locale 对应字符串；
 * initI18n() 在 main.ts 启动时调一次缓存，之后 t() 全部同步取词。
 *
 * 设计要点：
 * - locale 探测顺序：LC_ALL > LANG > 默认 en_US；
 * - POSIX 下划线形式解析：zh_CN.UTF-8 → zh_CN；en_US → en_US 原样；
 * - locale 加载 fallback 链：精确 locale → 语言级（zh_CN → zh）→ en_US；
 * - en_US 是兜底 locale，en_US.json 必须包含全部 key（见 manifest "i18n" 字段）；
 * - 占位符采用 printf 风格：{0} {1} 按参数序号替换，缺参用空串；
 * - t() 永不抛异常：未命中 key 返回 key 本身，dict 未初始化同样返回 key 本身。
 */

// locale 探测：LC_ALL > LANG > 默认 en_US。
// 解析 POSIX 形式：zh_CN.UTF-8 → zh_CN；en_US → en_US。
export function detectLocale(): string {
  let raw = "";
  try {
    const deno = (globalThis as any).Deno;
    if (typeof deno?.env?.get === "function") {
      raw = deno.env.get("LC_ALL") ?? deno.env.get("LANG") ?? "";
    } else {
      raw = process.env?.LC_ALL ?? process.env?.LANG ?? "";
    }
  } catch {
    // 环境变量读取失败（如未授权）时走默认 en_US
    return "en_US";
  }
  if (!raw) {
    return "en_US";
  }
  // 去掉 codeset 与 modifier：zh_CN.UTF-8 / zh_CN@pinyin → zh_CN
  const base = raw.split(".")[0].split("@")[0].trim();
  if (!base) {
    return "en_US";
  }
  // 规范为 POSIX 下划线形式：zh-CN → zh_CN
  return base.replace(/-/g, "_");
}

// 已加载 locale 字典的闭包缓存，避免重复读盘。
const LOCALES = new Map<string, Record<string, string>>();

// 加载单一 locale：从 ./i18n/<locale>.json 读 JSON。
// 缺文件时：若 locale != "en_US" 递归 fallback en_US（经语言级 zh_CN → zh），
// en_US 也缺则返空 dict。已加载的缓存到 LOCALES，避免重读。
async function loadLocale(loc: string): Promise<Record<string, string>> {
  const cached = LOCALES.get(loc);
  if (cached) {
    return cached;
  }
  // 文件位置策略：相对 import.meta.url 解析，保证 deno 运行时无论 cwd 在哪都能找到。
  const url = new URL(`./i18n/${loc}.json`, import.meta.url);
  let dict: Record<string, string> | null = null;
  try {
    const text = await Deno.readTextFile(url);
    const parsed = JSON.parse(text);
    if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
      dict = parsed as Record<string, string>;
    }
  } catch {
    // 文件缺失或 JSON 损坏：按缺文件处理，走 fallback 链
    dict = null;
  }
  if (dict) {
    LOCALES.set(loc, dict);
    return dict;
  }
  // fallback 链：zh_CN → zh → en_US；en_US 缺文件返空 dict（t() 将直接返 key）
  if (loc !== "en_US") {
    if (loc.includes("_")) {
      return await loadLocale(loc.split("_")[0]);
    }
    return await loadLocale("en_US");
  }
  const empty: Record<string, string> = {};
  return empty;
}

// 模块级缓存；initI18n() 后由 t() 同步读。
let _dict: Record<string, string> | null = null;
let _locale = "en_US";

// 当前生效 locale（供 buildSystemPrompt 等按 locale 选文案的调用方读取）。
export function currentLocale(): string {
  return _locale;
}

// 启动时调一次（从 main.ts 入口或 runCopilot 头）。
export async function initI18n(): Promise<void> {
  _locale = detectLocale();
  _dict = await loadLocale(_locale);
}

// 同步取翻译：命中 key 返回本地化串；未命中返回 key 本身（永不抛）。
// {0} {1} 占位符按 printf 风格替换；args[i] 缺则用空串。
export function t(key: string, ...args: unknown[]): string {
  const msg = _dict?.[key] ?? key;
  return msg.replace(/\{(\d+)\}/g, (_, i) => String(args[Number(i)] ?? ""));
}
