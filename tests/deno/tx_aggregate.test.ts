// W6/T29 跨模块集成聚合层:验证 copilot 事务通道全链路
//   state read(真文件)→ translate(注入捕获)→ classify → exec tx(mock 链)
// 行为符合 plan 验收。本文件是**最后一层**聚合断言:单模块行为已在
// main/exec/policy/audit/i18n 各自测试钉死,此处只验跨模块联动 +
// i18n/audit 防回退完整性断言。
//
// 继承的既有模式(不重写 setup/teardown):
// - T27:txBeginFn/txProposeFn/txPreviewFn/txApplyFn 4 注入 + 调用序记录
// - T28:DAEDALUS_STATE_PATH 基线锁 + withStateEnv 覆写还原
// - 聚合差异化:state 读取走**真文件真解析**(Deno.makeTempDir + 真
//   state.jsonl,readStateFn 不注入),tx 链走 mock 注入(绝 spawn 真二进制)。
import { expect } from "jsr:@std/expect@1";
import { runCopilot } from "../../daedalus/plugin/copilot/main.ts";
import { ALLOWED_AUDIT_TOOLS, type AuditTool } from "../../daedalus/plugin/copilot/audit.ts";
import { initI18n, t } from "../../daedalus/plugin/copilot/i18n.ts";
import type { TxApplyOutcome, TxJournal, TxPreviewOutcome, TxProposeOutcome, TxStep } from "../../daedalus/plugin/copilot/exec.ts";

// locale 基线锁(main.test.ts 同族纪律):断言基于 en_US 文案硬编码。
if ((globalThis as any).Deno?.env?.set) {
  Deno.env.set("LC_ALL", "en_US.UTF-8");
} else {
  process.env.LC_ALL = "en_US.UTF-8";
}

// state 基线锁(T28 同法):默认把 DAEDALUS_STATE_PATH 钉到不存在路径,
// 开发者真实 ~/.local/share 状态记忆绝不泄漏进断言;聚合用例各自覆写。
const BASELINE_STATE = "/tmp/daedalus-tx-aggregate-baseline-missing.jsonl";
if ((globalThis as any).Deno?.env?.set) {
  Deno.env.set("DAEDALUS_STATE_PATH", BASELINE_STATE);
}

let stdoutChunks: string[] = [];
let stderrChunks: string[] = [];
let auditLogs: Array<{ tool: string; args: any; outcome: string }> = [];

const mockStdout = { write: (text: string) => { stdoutChunks.push(text); } };
const mockStderr = { write: (text: string) => { stderrChunks.push(text); } };
const mockRecordAudit = async (tool: string, args: any, outcome: string) => {
  auditLogs.push({ tool, args, outcome });
  return true;
};

function setup() {
  stdoutChunks = [];
  stderrChunks = [];
  auditLogs = [];
}

// ── T27 同族 tx mock(4 注入 + 调用序记录)──────────────────────────────
const TX_ID = "f0e1d2c3b4a59687";

function txJournalFixture(status: string, steps: TxStep[]): TxJournal {
  return {
    id: TX_ID,
    created_at: "2026-09-07T00:00:00Z",
    status: status as TxJournal["status"],
    steps,
    rollback_plan: { steps: [] },
  };
}

const TX_STEP: TxStep = {
  index: 1,
  adapter: "service.set",
  args: { name: "sshd.service", desired_state: "stopped" },
  before_state: { ActiveState: "active" },
  after_state: { ActiveState: "inactive" },
  op_result: { returncode: 0 },
};

function makeTxMocks() {
  const calls: string[] = [];
  return {
    calls,
    txBeginFn: async () => {
      calls.push("begin");
      return { ok: true, txId: TX_ID } as const;
    },
    txProposeFn: async (_txId: string, _adapter: string, _args: unknown): Promise<TxProposeOutcome> => {
      calls.push("propose");
      return {
        ok: true,
        journal: txJournalFixture("proposed", [TX_STEP]),
        step: TX_STEP,
        beforeState: TX_STEP.before_state,
        afterState: TX_STEP.after_state,
      };
    },
    txPreviewFn: async (_txId: string): Promise<TxPreviewOutcome> => {
      calls.push("preview");
      return {
        ok: true,
        journal: txJournalFixture("proposed", [TX_STEP]),
        diff: [{
          index: 1,
          adapter: "service.set",
          args: TX_STEP.args,
          beforeState: TX_STEP.before_state,
          afterState: TX_STEP.after_state,
        }],
      };
    },
    txApplyFn: async (_txId: string): Promise<TxApplyOutcome> => {
      calls.push("apply");
      return { ok: true, journal: txJournalFixture("applied", [TX_STEP]), opResults: [{ returncode: 0 }] };
    },
  };
}

function txTranslate(command: string, args: string[], capture?: (systemContext?: string) => void) {
  return async (_q: string, _o?: unknown, systemContext?: string) => {
    capture?.(systemContext);
    return JSON.stringify({ command, args, explanation: "Apply desired_state to a user-scope service" });
  };
}

const txConfig = () => ({
  provider: "openai" as const,
  apiKey: "test-key",
  model: "gpt-4o-mini",
  baseUrl: "https://api.openai.com/v1",
});

// ── 真 state.jsonl 夹具(聚合层不 mock 状态读取)────────────────────────
async function writeRealStateFile(
  dir: string,
  entries: Array<{ name: string; active: string; sub: string; observedAt: string }>,
): Promise<string> {
  const file = `${dir}/state.jsonl`;
  const lines = entries.map((e) =>
    JSON.stringify({
      kind: "service",
      name: e.name,
      observed_at: e.observedAt,
      payload: {
        kind: "service",
        name: e.name,
        desired_state: "",
        properties: { ActiveState: e.active, SubState: e.sub },
      },
    })
  );
  await Deno.writeTextFile(file, lines.join("\n") + "\n");
  return file;
}

async function withStateEnv(path: string, fn: () => Promise<void>): Promise<void> {
  const prev = Deno.env.get("DAEDALUS_STATE_PATH");
  Deno.env.set("DAEDALUS_STATE_PATH", path);
  try {
    await fn();
  } finally {
    if (prev === undefined) Deno.env.delete("DAEDALUS_STATE_PATH");
    else Deno.env.set("DAEDALUS_STATE_PATH", prev);
  }
}

// ═══════════════════════════════════════════════════════════════════════
// 聚合断言 1-5:状态注入 × 事务提议 联动(真 state 文件读 → 事务通道分流)
// ═══════════════════════════════════════════════════════════════════════

Deno.test("Aggregate T29 - real state file feeds KNOWN STATE into translate; L0 tx.propose runs begin->propose->preview->y->apply", async () => {
  await initI18n();
  const dir = await Deno.makeTempDir({ prefix: "t29-state-tx-l0-" });
  const stateFile = await writeRealStateFile(dir, [
    { name: "sshd.service", active: "active", sub: "running", observedAt: "2026-09-07T02:00:00Z" },
  ]);

  await withStateEnv(stateFile, async () => {
    setup();
    const tx = makeTxMocks();
    let capturedSystemContext: string | undefined;

    const code = await runCopilot({
      query: "stop the sshd service",
      isTerminal: true,
      stdout: mockStdout,
      stderr: mockStderr,
      stdinReader: async () => "y",
      translateFn: txTranslate("tx.propose", ["sshd.service", "stopped"], (ctx) => {
        capturedSystemContext = ctx;
      }),
      execFn: async () => ({ stdout: "", stderr: "", returncode: 0, error: null }),
      recordAuditFn: mockRecordAudit,
      readConfigFn: txConfig,
      txBeginFn: tx.txBeginFn,
      txProposeFn: tx.txProposeFn,
      txPreviewFn: tx.txPreviewFn,
      txApplyFn: tx.txApplyFn,
    });

    // 联动①:真文件 → readStateSummary 解析 → systemContext 含该观测
    expect(capturedSystemContext).toBeDefined();
    expect(capturedSystemContext!.toLowerCase()).toContain("known state");
    expect(capturedSystemContext).toContain("sshd.service: ActiveState=active, SubState=running (observed 2026-09-07T02:00:00Z)");
    // 联动②:detectTxIntent 命中 → 事务通道五步严格调用序
    expect(code).toBe(0);
    expect(tx.calls).toEqual(["begin", "propose", "preview", "apply"]);
    const allStdout = stdoutChunks.join("");
    expect(allStdout).toContain(`Planned changes (transaction ${TX_ID}`);
    expect(allStdout).toContain(`Transaction ${TX_ID} applied`);
    // 联动③:审计链 translate(tx_propose)→ tx_propose → tx_apply(auto:false)
    const translateLog = auditLogs.find((l) => l.tool === "copilot_translate");
    expect(translateLog?.args.tx_kind).toBe("tx_propose");
    expect(translateLog?.args.risk_level).toBe("safe");
    const proposeLog = auditLogs.find((l) => l.tool === "copilot_tx_propose");
    expect(proposeLog?.outcome).toBe("success");
    expect(proposeLog?.args.target).toBe("sshd.service");
    expect(proposeLog?.args.desired_state).toBe("stopped");
    const applyLog = auditLogs.find((l) => l.tool === "copilot_tx_apply");
    expect(applyLog?.args.auto).toBe(false);
    // shell 通道绝不参与
    expect(auditLogs.some((l) => l.tool === "copilot_confirm")).toBe(false);
    expect(stderrChunks.join("")).toBe("");
  });

  await Deno.remove(dir, { recursive: true });
});

Deno.test("Aggregate T29 - real state + L1 tx.apply restarted is display-only: no begin, no propose, no apply", async () => {
  await initI18n();
  const dir = await Deno.makeTempDir({ prefix: "t29-state-tx-l1-" });
  const stateFile = await writeRealStateFile(dir, [
    { name: "sshd.service", active: "active", sub: "running", observedAt: "2026-09-07T03:00:00Z" },
  ]);

  await withStateEnv(stateFile, async () => {
    setup();
    const tx = makeTxMocks();
    let readLineCalls = 0;
    let capturedSystemContext: string | undefined;

    const code = await runCopilot({
      query: "restart sshd",
      isTerminal: true,
      stdout: mockStdout,
      stderr: mockStderr,
      stdinReader: async () => {
        readLineCalls++;
        return "y";
      },
      translateFn: txTranslate("tx.apply", ["sshd.service", "restarted"], (ctx) => {
        capturedSystemContext = ctx;
      }),
      execFn: async () => ({ stdout: "", stderr: "", returncode: 0, error: null }),
      recordAuditFn: mockRecordAudit,
      readConfigFn: txConfig,
      txBeginFn: tx.txBeginFn,
      txProposeFn: tx.txProposeFn,
      txPreviewFn: tx.txPreviewFn,
      txApplyFn: tx.txApplyFn,
    });

    // 状态注入照常生效,但 caution 事务绝不进执行通道(连 begin 都不发起)
    expect(capturedSystemContext).toContain("sshd.service: ActiveState=active");
    expect(code).toBe(0);
    expect(readLineCalls).toBe(0);
    expect(tx.calls).toEqual([]);
    const allStdout = stdoutChunks.join("");
    expect(allStdout).toContain("⚠");
    expect(allStdout).toContain("→ tx.apply sshd.service restarted");
    expect(allStdout).toContain("Please run this command in your terminal");
    const rejectLog = auditLogs.find((l) => l.tool === "copilot_reject");
    expect(rejectLog?.args.risk).toBe("caution");
    expect(rejectLog?.args.tx_kind).toBe("tx_apply");
    expect(rejectLog?.args.desired_state).toBe("restarted");
    expect(auditLogs.some((l) => l.tool.startsWith("copilot_tx_"))).toBe(false);
  });

  await Deno.remove(dir, { recursive: true });
});

Deno.test("Aggregate T29 - state read I/O failure degrades gracefully: empty context, translation proceeds, L0 tx chain completes", async () => {
  await initI18n();
  const dir = await Deno.makeTempDir({ prefix: "t29-state-io-err-" });

  // 把 DAEDALUS_STATE_PATH 指向**目录** → 非 NotFound 的真实 I/O 错
  // (root 下 chmod 000 恒放行,目录读取错误分类与 euid 无关,更稳)。
  await withStateEnv(dir, async () => {
    setup();
    const tx = makeTxMocks();
    let capturedSystemContext: string | undefined = "sentinel-not-called";

    const code = await runCopilot({
      query: "stop sshd",
      isTerminal: true,
      stdout: mockStdout,
      stderr: mockStderr,
      stdinReader: async () => "y",
      translateFn: txTranslate("tx.propose", ["sshd.service", "stopped"], (ctx) => {
        capturedSystemContext = ctx;
      }),
      execFn: async () => ({ stdout: "", stderr: "", returncode: 0, error: null }),
      recordAuditFn: mockRecordAudit,
      readConfigFn: txConfig,
      txBeginFn: tx.txBeginFn,
      txProposeFn: tx.txProposeFn,
      txPreviewFn: tx.txPreviewFn,
      txApplyFn: tx.txApplyFn,
    });

    // 降级三钉:① 翻译继续且 systemContext 为空(未注入 → undefined);
    // ② 错误路径经 state.summary.error 打 stderr(开发者可见);
    // ③ 事务 L0 happy path 不受状态读取失败影响,五步照常走完。
    expect(code).toBe(0);
    expect(capturedSystemContext).toBeUndefined();
    expect(stderrChunks.join("")).toContain(`copilot: state read permission denied: ${dir}`);
    expect(tx.calls).toEqual(["begin", "propose", "preview", "apply"]);
    expect(stdoutChunks.join("")).toContain(`Transaction ${TX_ID} applied`);
    // state 读取零审计事件(派生缓存≠证据)
    expect(auditLogs.some((l) => l.tool.includes("state"))).toBe(false);
  });

  await Deno.remove(dir, { recursive: true });
});

Deno.test("Aggregate T29 - non-TTY auto-authorized L0 tx with real state read records copilot_tx_apply auto=true", async () => {
  await initI18n();
  const dir = await Deno.makeTempDir({ prefix: "t29-state-tx-auto-" });
  const stateFile = await writeRealStateFile(dir, [
    { name: "nginx.service", active: "inactive", sub: "dead", observedAt: "2026-09-07T04:00:00Z" },
  ]);

  await withStateEnv(stateFile, async () => {
    setup();
    const tx = makeTxMocks();
    let capturedSystemContext: string | undefined;

    const code = await runCopilot({
      query: "start nginx",
      isTerminal: false,
      yes: true,
      stdout: mockStdout,
      stderr: mockStderr,
      translateFn: txTranslate("tx.apply", ["nginx.service", "started"], (ctx) => {
        capturedSystemContext = ctx;
      }),
      execFn: async () => ({ stdout: "", stderr: "", returncode: 0, error: null }),
      recordAuditFn: mockRecordAudit,
      readConfigFn: txConfig,
      txBeginFn: tx.txBeginFn,
      txProposeFn: tx.txProposeFn,
      txPreviewFn: tx.txPreviewFn,
      txApplyFn: tx.txApplyFn,
    });

    expect(code).toBe(0);
    expect(capturedSystemContext).toContain("nginx.service: ActiveState=inactive");
    expect(tx.calls).toEqual(["begin", "propose", "preview", "apply"]);
    const applyLog = auditLogs.find((l) => l.tool === "copilot_tx_apply");
    expect(applyLog?.args.auto).toBe(true); // 非 TTY = 系统自动授权
    expect(auditLogs.some((l) => l.tool === "copilot_tx_reject")).toBe(false);
  });

  await Deno.remove(dir, { recursive: true });
});

Deno.test("Aggregate T29 - user 'n' after real state read drops L0 tx: begin/propose/preview happened, apply never, copilot_tx_reject on chain", async () => {
  await initI18n();
  const dir = await Deno.makeTempDir({ prefix: "t29-state-tx-n-" });
  const stateFile = await writeRealStateFile(dir, [
    { name: "sshd.service", active: "active", sub: "running", observedAt: "2026-09-07T05:00:00Z" },
  ]);

  await withStateEnv(stateFile, async () => {
    setup();
    const tx = makeTxMocks();

    const code = await runCopilot({
      query: "stop sshd please",
      isTerminal: true,
      stdout: mockStdout,
      stderr: mockStderr,
      stdinReader: async () => "n",
      translateFn: txTranslate("tx.propose", ["sshd.service", "stopped"]),
      execFn: async () => ({ stdout: "", stderr: "", returncode: 0, error: null }),
      recordAuditFn: mockRecordAudit,
      readConfigFn: txConfig,
      txBeginFn: tx.txBeginFn,
      txProposeFn: tx.txProposeFn,
      txPreviewFn: tx.txPreviewFn,
      txApplyFn: tx.txApplyFn,
    });

    expect(code).toBe(0);
    expect(tx.calls).toEqual(["begin", "propose", "preview"]); // apply 绝无
    const rejectLog = auditLogs.find((l) => l.tool === "copilot_tx_reject");
    expect(rejectLog?.outcome).toBe("denied");
    expect(rejectLog?.args.reason).toBe("user denied tx");
    expect(rejectLog?.args.tx_id).toBe(TX_ID);
    expect(stdoutChunks.join("")).toContain("Transaction dropped");
    expect(auditLogs.some((l) => l.tool === "copilot_tx_apply")).toBe(false);
  });

  await Deno.remove(dir, { recursive: true });
});

// ═══════════════════════════════════════════════════════════════════════
// 聚合断言 6-7:i18n 完整性(纯 Deno 实现,无 jq shell 调用)
// ═══════════════════════════════════════════════════════════════════════

async function loadLocaleDict(locale: string): Promise<Record<string, string>> {
  const url = new URL(`../../daedalus/plugin/copilot/i18n/${locale}.json`, import.meta.url);
  return JSON.parse(await Deno.readTextFile(url)) as Record<string, string>;
}

Deno.test("Aggregate T29 - i18n key-set parity: en_US and zh_CN keys are strictly identical (no jq, diff-of-sets empty both ways)", async () => {
  const en = await loadLocaleDict("en_US");
  const zh = await loadLocaleDict("zh_CN");
  const enKeys = Object.keys(en).sort();
  const zhKeys = Object.keys(zh).sort();
  const onlyEn = enKeys.filter((k) => !(k in zh));
  const onlyZh = zhKeys.filter((k) => !(k in en));
  expect(onlyEn).toEqual([]);
  expect(onlyZh).toEqual([]);
  expect(enKeys.length).toBeGreaterThan(0);
  expect(enKeys.length).toBe(zhKeys.length);
  // T30 全集下限:79 键(tx + state 通道补齐后的既有规模)
  expect(enKeys.length).toBeGreaterThanOrEqual(79);
});

Deno.test("Aggregate T29 - every t(\"literal\") in main.ts has a translation in both locales (zero raw-key fallback)", async () => {
  const mainSrc = await Deno.readTextFile(
    new URL("../../daedalus/plugin/copilot/main.ts", import.meta.url),
  );
  // (?<![\w$]) 排除 get("/split(" 等尾缀 t 的误报;只取字面量首参键名
  const re = /(?<![\w$])t\(\s*"([^"]+)"/g;
  const keys = [...new Set([...mainSrc.matchAll(re)].map((m) => m[1]))];
  expect(keys.length).toBeGreaterThanOrEqual(50);

  const en = await loadLocaleDict("en_US");
  const zh = await loadLocaleDict("zh_CN");
  const missingEn = keys.filter((k) => !(k in en));
  const missingZh = keys.filter((k) => !(k in zh));
  expect(missingEn).toEqual([]);
  expect(missingZh).toEqual([]);

  // 运行时零兜底:LC_ALL 已钉 en_US,t(key) 绝不返回原 key
  await initI18n();
  const rawFallback = keys.filter((k) => t(k) === k);
  expect(rawFallback).toEqual([]);
});

// ═══════════════════════════════════════════════════════════════════════
// 聚合断言 8-9:audit 事件白名单完整性(防 T30 之后回退)
// ═══════════════════════════════════════════════════════════════════════

Deno.test("Aggregate T29 - ALLOWED_AUDIT_TOOLS contains the tx event trio and the legacy six (regression guard)", async () => {
  const required = [
    "copilot_translate",
    "copilot_reject",
    "copilot_confirm",
    "copilot_edit",
    "copilot_cancel",
    "copilot_error",
    "copilot_tx_propose",
    "copilot_tx_apply",
    "copilot_tx_reject",
  ];
  for (const tool of required) {
    expect(ALLOWED_AUDIT_TOOLS.has(tool)).toBe(true);
  }
  // tx 三事件是 T30 落地的防回退核心:单独再钉一遍(语义显式)
  expect(ALLOWED_AUDIT_TOOLS.has("copilot_tx_propose")).toBe(true);
  expect(ALLOWED_AUDIT_TOOLS.has("copilot_tx_apply")).toBe(true);
  expect(ALLOWED_AUDIT_TOOLS.has("copilot_tx_reject")).toBe(true);
});

Deno.test("Aggregate T29 - AuditTool union stays in sync with ALLOWED_AUDIT_TOOLS (type-level exhaustive + set equality)", async () => {
  // 类型级收口:Record<AuditTool,true> 必须逐成员给键——union 增员不改此表
  // 编译即红(deno check/test 类型门);union 删员则字面量键报错。
  const unionExhaustive: Record<AuditTool, true> = {
    copilot_translate: true,
    copilot_reject: true,
    copilot_confirm: true,
    copilot_edit: true,
    copilot_cancel: true,
    copilot_error: true,
    copilot_tx_propose: true,
    copilot_tx_apply: true,
    copilot_tx_reject: true,
  };
  const unionMembers = Object.keys(unionExhaustive);
  // 双向集合相等:白名单 ⊆ union 且 union ⊆ 白名单(任何一侧漂移即红)
  for (const m of unionMembers) {
    expect(ALLOWED_AUDIT_TOOLS.has(m as string)).toBe(true);
  }
  expect([...ALLOWED_AUDIT_TOOLS].sort()).toEqual([...unionMembers].sort());
});
