/**
 * 关键不变式：locale 探测 LC_ALL > LANG > en_US；fallback 链 精确 locale →
 * 语言级（zh_CN → zh）→ en_US（en_US.json 必含全部 key）；占位符 {0} {1}
 * 按序号替换、缺参用空串；t() 永不抛异常，未命中 key 返回 key 本身。
 */

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
  // zh_CN.UTF-8 / zh_CN@pinyin → zh_CN；zh-CN → zh_CN
  const base = raw.split(".")[0].split("@")[0].trim();
  if (!base) {
    return "en_US";
  }
  return base.replace(/-/g, "_");
}

const LOCALES = new Map<string, Record<string, string>>();

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
  if (loc !== "en_US") {
    if (loc.includes("_")) {
      return await loadLocale(loc.split("_")[0]);
    }
    return await loadLocale("en_US");
  }
  const empty: Record<string, string> = {};
  return empty;
}

let _dict: Record<string, string> | null = null;
let _locale = "en_US";

// 当前生效 locale（供 buildSystemPrompt 等按 locale 选文案的调用方读取）。
export function currentLocale(): string {
  return _locale;
}

export async function initI18n(): Promise<void> {
  _locale = detectLocale();
  _dict = await loadLocale(_locale);
}

export function t(key: string, ...args: unknown[]): string {
  const msg = _dict?.[key] ?? key;
  return msg.replace(/\{(\d+)\}/g, (_, i) => String(args[Number(i)] ?? ""));
}
