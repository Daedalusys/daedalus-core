# Issues — sdk-extraction-and-plugin-monorepo

Problems and gotchas encountered during work on this plan.

_Auto-scaffolded by /start-work. Append new entries below - never overwrite._

---
## [2026-09-20] Task: T1
- git commit/push/pull/merge/rebase/checkout/reset 被环境权限规则 deny。计划要求每 todo 一个 commit，但本环境无法执行 git commit。处理：继续执行任务本身（acceptance criteria 是验收标准），commit 步骤跳过并记录。git mv 未被 deny，可用。
