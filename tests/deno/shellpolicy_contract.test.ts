// 跨语言契约测试(Go ↔ Deno):copilot policy.ts 的冻结副本必须与
// daedalus/core/internal/shellpolicy 的默认常量、以及单一事实源
// daedalus/files/system/opt/daedalus/shared/policy.toml 三点一致。
// (计划 todo 14:替代已随 task 5 删除的 py↔deno parity 测试。)
//
// ⚠ 同步义务:任何人修改 Go 侧 internal/shellpolicy 或 policy.toml 的
// 白名单/前缀/黑名单集合时,必须同步修改 policy.ts 冻结副本,否则本文件必红。
// 本测试通过直接解析 Go 源文件与 TOML 的字符串字面量做集合比对,
// 不依赖 Go 工具链,保证 `deno test` 单独可跑。
import { expect } from "jsr:@std/expect@1";
import {
  ALLOWED_PATH_PREFIXES,
  BLOCKED_PATHS,
  DEFAULT_ALLOW_COMMANDS,
} from "../../daedalus/plugin/copilot/policy.ts";

// 仓库根:本文件位于 <root>/tests/deno/,上溯两级。
const repoRoot = new URL("../../", import.meta.url);
const goSourceUrl = new URL(
  "daedalus/core/internal/shellpolicy/shellpolicy.go",
  repoRoot,
);
const policyTomlUrl = new URL(
  "daedalus/files/system/opt/daedalus/shared/policy.toml",
  repoRoot,
);

/** 从 Go 源码中提取 `var <name> = map[string]struct{}{...}` 或 `[]string{...}` 内的字符串字面量。 */
function extractGoLiterals(src: string, declName: string): string[] {
  // 匹配 var <declName> = map[string]struct{}{ ... } 或 = []string{ ... } 直到闭合大括号。
  const re = new RegExp(
    `var\\s+${declName}\\s*=\\s*(?:map\\[string\\]struct\\{\\}|\\[\\]string)\\{([\\s\\S]*?)\\n\\}`,
  );
  const m = re.exec(src);
  if (!m) {
    throw new Error(`在 Go 源码中找不到声明: ${declName}(格式变动需同步本解析器)`);
  }
  return [...m[1].matchAll(/"([^"]*)"/g)].map((q) => q[1]);
}

/** 从 policy.toml 中提取 `<key> = [ ... ]` 行内数组的字符串字面量。 */
function extractTomlArray(src: string, key: string): string[] {
  const re = new RegExp(`^${key}\\s*=\\s*\\[([^\\]]*)\\]`, "m");
  const m = re.exec(src);
  if (!m) {
    throw new Error(`在 policy.toml 中找不到键: ${key}`);
  }
  return [...m[1].matchAll(/"([^"]*)"/g)].map((q) => q[1]);
}

const sorted = (xs: readonly string[]): string[] => [...xs].sort();

Deno.test("跨语言契约 - policy.ts 冻结 ALLOW_COMMANDS 恰为 15 项(与 internal/shellpolicy 默认一致)", () => {
  expect(DEFAULT_ALLOW_COMMANDS.size).toBe(15);
});

Deno.test("跨语言契约 - policy.ts DEFAULT_ALLOW_COMMANDS == Go DefaultAllowCommands(shellpolicy.go)", async () => {
  const goSrc = await Deno.readTextFile(goSourceUrl);
  const goCommands = extractGoLiterals(goSrc, "DefaultAllowCommands");
  expect(sorted(goCommands)).toEqual(sorted([...DEFAULT_ALLOW_COMMANDS]));
});

Deno.test("跨语言契约 - policy.ts DEFAULT_ALLOW_COMMANDS == policy.toml [shell].allowed_commands(单一事实源)", async () => {
  const tomlSrc = await Deno.readTextFile(policyTomlUrl);
  const tomlCommands = extractTomlArray(tomlSrc, "allowed_commands");
  expect(sorted(tomlCommands)).toEqual(sorted([...DEFAULT_ALLOW_COMMANDS]));
});

Deno.test("跨语言契约 - policy.ts ALLOWED_PATH_PREFIXES == Go AllowedPathPrefixes == policy.toml", async () => {
  const goSrc = await Deno.readTextFile(goSourceUrl);
  const tomlSrc = await Deno.readTextFile(policyTomlUrl);
  const goPrefixes = extractGoLiterals(goSrc, "AllowedPathPrefixes");
  const tomlPrefixes = extractTomlArray(tomlSrc, "allowed_path_prefixes");
  // 9 项前缀:TS 冻结副本与两侧事实源逐字一致(TS/Go 均为有序列表,按序比对)。
  expect(ALLOWED_PATH_PREFIXES.length).toBe(9);
  expect(ALLOWED_PATH_PREFIXES).toEqual(goPrefixes);
  expect(ALLOWED_PATH_PREFIXES).toEqual(tomlPrefixes);
});

Deno.test("跨语言契约 - policy.ts BLOCKED_PATHS == Go BlockedPaths == policy.toml", async () => {
  const goSrc = await Deno.readTextFile(goSourceUrl);
  const tomlSrc = await Deno.readTextFile(policyTomlUrl);
  const goBlocked = extractGoLiterals(goSrc, "BlockedPaths");
  const tomlBlocked = extractTomlArray(tomlSrc, "blocked_paths");
  // 5 项敏感路径黑名单:三源逐字一致。
  expect(BLOCKED_PATHS.length).toBe(5);
  expect(BLOCKED_PATHS).toEqual(goBlocked);
  expect(BLOCKED_PATHS).toEqual(tomlBlocked);
});

Deno.test("跨语言契约 - ALLOW_COMMANDS 环境变量为整体替换(REPLACE)语义,与 Go ResolveAllowCommands 一致", async () => {
  // policy.ts 在模块加载时一次性解析 ALLOW_COMMANDS,因此必须在子进程
  // 中注入环境变量后重新求值,才是对真实代码路径的断言(而非测试内复刻)。
  // 语义契约(Go shellpolicy.ResolveAllowCommands 文档同源):
  //   非空 env → 逗号分隔、逐项 trim、丢弃空项,整体替换默认集(不取并集);
  //   空/缺省 env → 回退默认 15 项。
  const policyUrl = new URL("daedalus/plugin/copilot/policy.ts", repoRoot);
  const probe = `
    const { ALLOW_COMMANDS, DEFAULT_ALLOW_COMMANDS } = await import("${policyUrl.href}");
    console.log(JSON.stringify({
      allowed: [...ALLOW_COMMANDS].sort(),
      defaultSize: DEFAULT_ALLOW_COMMANDS.size,
    }));
  `;

  // 用例 1:显式 env(含空白项与尾随逗号)→ 精确替换集。
  // 注:不清空继承环境(deno 子进程自身需要 HOME),但 ALLOW_COMMANDS 每次显式
  // 设值,父进程中残留的同名变量不会污染断言。
  const replaced = await new Deno.Command(Deno.execPath(), {
    args: ["eval", probe],
    env: { ALLOW_COMMANDS: " rm , echo ,,cat" },
    stdout: "piped",
    stderr: "piped",
  }).output();
  expect(replaced.code).toBe(0);
  const replacedJson = JSON.parse(new TextDecoder().decode(replaced.stdout));
  expect(replacedJson.allowed).toEqual(["cat", "echo", "rm"]); // REPLACE:df 等默认项不复存在
  expect(replacedJson.defaultSize).toBe(15);

  // 用例 2:env 为空字符串 → 与 Go(envValue == "" → 默认副本)一致的兜底语义。
  const fallback = await new Deno.Command(Deno.execPath(), {
    args: ["eval", probe],
    env: { ALLOW_COMMANDS: "" },
    stdout: "piped",
    stderr: "piped",
  }).output();
  expect(fallback.code).toBe(0);
  const fallbackJson = JSON.parse(new TextDecoder().decode(fallback.stdout));
  expect(fallbackJson.allowed).toEqual(sorted([...DEFAULT_ALLOW_COMMANDS]));
});
