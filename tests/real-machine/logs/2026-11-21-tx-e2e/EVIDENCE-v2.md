
## preflight:环境能力
- uid=1000 user=udefine-dev systemd=systemd 259 (259.10-1.fc44)
- systemctl --user 可用(有活着的用户会话)
- ✅ preflight:systemctl --user
- sudo 免密可用

## 准备:/run/daedalus 目录与签发者组
- 第二用户: daedalus-e2e-other(uid=967)
drwxr-xr-x. 5 root root             100 Nov 21 18:07 /run/daedalus
drwxr-xr-x. 2 root root              40 Nov 21 22:42 /run/daedalus/approvals
drwxrwxr-t. 2 root daedalus-signers  40 Nov 21 22:42 /run/daedalus/grants
drwxrwxrwt. 2 root root              40 Nov 21 22:42 /run/daedalus/pending

## 准备:档位表 /etc/daedalus/agent-autonomy.toml
- 档位表已装: auto=daedalus-tx-e2e-auto.service / confirm=daedalus-tx-e2e-confirm.service / human=daedalus-tx-e2e-human.service

## 准备:用户域测试单元
UNIT FILE                       STATE   PRESET
daedalus-tx-e2e-auto.service    enabled disabled
daedalus-tx-e2e-confirm.service enabled disabled
daedalus-tx-e2e-human.service   enabled disabled

3 unit files listed.

## 场景 1(第一档 auto):预览 + 执行,单元应真的起来
- tx_preview → auto
- ✅ 场景1:preview 判为 auto
- tx_apply → applied
- ✅ 场景1:apply 判为 applied
- ✅ 场景1:单元真的在跑

## 场景 2(第二档 confirm):无条子被拒 → 补条子 → 放行
R2 原始: {
  "jsonrpc": "2.0",
  "id": 2,
  "result": {
    "content": [
      {
        "type": "text",
        "text": "{\n  \"error\": \"grant_required\",\n  \"tier\": \"confirm\",\n  \"required_scope\": [\n    \"systemd:user:restart:daedalus-tx-e2e-confirm.service\"\n  ],\n  \"reason\": \"需要授权条子\",\n  \"detail\": \"请让有权签发的人执行 daedalus-tx grant add --for udefine-dev --scope systemd:user:restart:daedalus-tx-e2e-confirm.service,然后把条子 id 作为 grant_id 传入\"\n}"
      }
    ],
    "isError": true
  }
}
- 无条子 apply → error=grant_required required_scope=["systemd:user:restart:daedalus-tx-e2e-confirm.service"]
- ✅ 场景2:无条子被拒
- ✅ 场景2:给出所需范围
- ✅ 场景2:被拒后单元没动

```
{"id":"g-20261121-ffca1a4f","path":"/run/daedalus/grants/g-20261121-ffca1a4f.json","scope":["systemd:user:restart:daedalus-tx-e2e-confirm.service"],"issued_by":"udefine-dev","issued_at":"2026-11-21T14:42:20Z","expires_at":"2026-11-21T14:52:20Z","max_uses":1}
```
退出码: 0
- 条子 id = g-20261121-ffca1a4f
- 条子文件: /run/daedalus/grants/g-20261121-ffca1a4f.json
-rw-r--r--. 1 udefine-dev daedalus-signers 214 Nov 21 22:42 /run/daedalus/grants/g-20261121-ffca1a4f.json
- ✅ 条子属主=真实发起人
R2B 原始: {
  "jsonrpc": "2.0",
  "id": 2,
  "result": {
    "content": [
      {
        "type": "text",
        "text": "{\n  \"error\": \"grant_usage_failed\",\n  \"tier\": \"confirm\",\n  \"reason\": \"条子用量记账失败,拒绝执行\",\n  \"detail\": \"用量账本 /home/udefine-dev/.local/share/daedalus/tx/usage-1000.jsonl 落在用户 HOME(/home/udefine-dev)之下:用量账本不得落在用户 HOME(可被自行截断),拒绝执行\"\n}"
      }
    ],
    "isError": true
  }
}
- 账本落 HOME 时 apply → error=grant_usage_failed
- ✅ 场景2a:账本落 HOME ⇒ 拒绝执行
- ✅ 场景2a:被拒后单元没动
R2C 原始: {
  "jsonrpc": "2.0",
  "id": 2,
  "result": {
    "content": [
      {
        "type": "text",
        "text": "{\n  \"tx_id\": \"d810b5c6af1200a2\",\n  \"tier\": \"confirm\",\n  \"outcome\": \"applied\",\n  \"grant_id\": \"g-20261121-ffca1a4f\",\n  \"argv\": [\n    \"systemctl\",\n    \"--user\",\n    \"restart\",\n    \"daedalus-tx-e2e-confirm.service\"\n  ],\n  \"scope\": \"systemd:user:restart:daedalus-tx-e2e-confirm.service\"\n}"
      }
    ]
  }
}
- 账本在系统路径 apply → outcome=applied grant_id=g-20261121-ffca1a4f
- ✅ 场景2b:有匹配条子放行

```
{"id":"g-20261121-a589787f","path":"/run/daedalus/grants/g-20261121-a589787f.json","scope":["systemd:user:restart:daedalus-tx-e2e-confirm.service"],"issued_by":"root","issued_at":"2026-11-21T14:42:21Z","expires_at":"2026-11-21T14:52:21Z","max_uses":1,"for_uid":1000,"for_user":"udefine-dev"}
```
退出码: 0
- ✅ 场景2c:root 签 --for 成功
- ✅ 场景2c:root 签的条子属主=root
- ✅ 场景2c:for_uid 指向本用户

`$ sudo env -u SUDO_USER /home/udefine-dev/tx-e2e-v2/bin/daedalus-tx grant add --scope systemd:user:restart:daedalus-tx-e2e-confirm.service --ttl 10m --count 1 `

```
{"error":"root 签发必须写明给谁(--for \u003cuser\u003e)"}
daedalus-tx: root 签发必须写明给谁(--for <user>)
```
退出码: 2
- ✅ 场景2c:root 不写 --for 被拒(退出码 2)
R2Y 原始: {
  "jsonrpc": "2.0",
  "id": 2,
  "result": {
    "content": [
      {
        "type": "text",
        "text": "{\n  \"tx_id\": \"6b887a1d0c7182b9\",\n  \"tier\": \"confirm\",\n  \"outcome\": \"applied\",\n  \"grant_id\": \"g-20261121-a589787f\",\n  \"argv\": [\n    \"systemctl\",\n    \"--user\",\n    \"restart\",\n    \"daedalus-tx-e2e-confirm.service\"\n  ],\n  \"scope\": \"systemd:user:restart:daedalus-tx-e2e-confirm.service\"\n}"
      }
    ]
  }
}
- ✅ 场景2c:root 给本用户签的条子放行

## 场景 2d(绑定):给别人签的条子本用户用不了 —— 真机越权的那条路径
- 给 daedalus-e2e-other(uid=967)签的条子 id = g-20261121-2f0ade87
R2X 原始: {
  "jsonrpc": "2.0",
  "id": 2,
  "result": {
    "content": [
      {
        "type": "text",
        "text": "{\n  \"error\": \"grant_for_other_user\",\n  \"tier\": \"confirm\",\n  \"available_grants\": [\n    {\n      \"id\": \"g-20261121-a589787f\",\n      \"scope\": [\n        \"systemd:user:restart:daedalus-tx-e2e-confirm.service\"\n      ],\n      \"expires_at\": \"2026-11-21T14:52:21Z\",\n      \"max_uses\": 1,\n      \"used\": 1,\n      \"remaining\": 0,\n      \"expired\": false\n    },\n    {\n      \"id\": \"g-20261121-ffca1a4f\",\n      \"scope\": [\n        \"systemd:user:restart:daedalus-tx-e2e-confirm.service\"\n      ],\n      \"expires_at\": \"2026-11-21T14:52:20Z\",\n      \"max_uses\": 1,\n      \"used\": 1,\n      \"remaining\": 0,\n      \"expired\": false\n    }\n  ],\n  \"reason\": \"条子不是给你的\",\n  \"detail\": \"grant: 这张条子是给 \\\"daedalus-e2e-other\\\"(uid=967)的,不是给 \\\"udefine-dev\\\"(uid=1000)的\"\n}"
      }
    ],
    "isError": true
  }
}
- ✅ 场景2d:借用别人的条子被拒
- ✅ 场景2d:被拒后单元没动

## 场景 2e(可发现):agent 能自己列出「手上能用哪些条子」
tx_grants(本用户) 原始: {
  "jsonrpc": "2.0",
  "id": 2,
  "result": {
    "content": [
      {
        "type": "text",
        "text": "{\n  \"executor\": {\n    \"uid\": 1000,\n    \"name\": \"udefine-dev\"\n  },\n  \"grants\": [\n    {\n      \"id\": \"g-20261121-a589787f\",\n      \"scope\": [\n        \"systemd:user:restart:daedalus-tx-e2e-confirm.service\"\n      ],\n      \"expires_at\": \"2026-11-21T14:52:21Z\",\n      \"max_uses\": 1,\n      \"used\": 1,\n      \"remaining\": 0,\n      \"expired\": false\n    },\n    {\n      \"id\": \"g-20261121-ffca1a4f\",\n      \"scope\": [\n        \"systemd:user:restart:daedalus-tx-e2e-confirm.service\"\n      ],\n      \"expires_at\": \"2026-11-21T14:52:20Z\",\n      \"max_uses\": 1,\n      \"used\": 1,\n      \"remaining\": 0,\n      \"expired\": false\n    }\n  ],\n  \"invalid\": []\n}"
      }
    ]
  }
}
- ✅ 场景2e:清单里的执行者=本用户
- ✅ 场景2e:本用户可用的条子在清单里
- ✅ 场景2e:别人的条子不出现在清单里
tx_grants(daedalus-e2e-other) 原始: {
  "jsonrpc": "2.0",
  "id": 2,
  "result": {
    "content": [
      {
        "type": "text",
        "text": "{\n  \"executor\": {\n    \"uid\": 967,\n    \"name\": \"daedalus-e2e-other\"\n  },\n  \"grants\": [\n    {\n      \"id\": \"g-20261121-2f0ade87\",\n      \"scope\": [\n        \"systemd:user:restart:daedalus-tx-e2e-confirm.service\"\n      ],\n      \"expires_at\": \"2026-11-21T14:52:21Z\",\n      \"max_uses\": 1,\n      \"used\": 0,\n      \"remaining\": 1,\n      \"expired\": false\n    }\n  ],\n  \"invalid\": []\n}"
      }
    ]
  }
}
- ✅ 场景2f:第二个用户的执行者身份=自己
- ✅ 场景2f:第二个用户看不到本用户的条子
- ✅ 场景2f:第二个用户能看到给他签的条子

## 场景 3(第三档 human):出待办 → 无批准不执行 → 人批准 → 执行
R3 原始: {
  "jsonrpc": "2.0",
  "id": 2,
  "result": {
    "content": [
      {
        "type": "text",
        "text": "{\n  \"error\": \"human_approval_required\",\n  \"tier\": \"human\",\n  \"pending_id\": \"p-0e693a34e02f8589\",\n  \"argv\": [\n    \"systemctl\",\n    \"--user\",\n    \"restart\",\n    \"daedalus-tx-e2e-human.service\"\n  ],\n  \"argv_sha256\": \"df1b185c6ef3c73114119a1fb09d936027eacfe61e2f68cde5005bf40667e07b\",\n  \"reason\": \"需要人批准\",\n  \"detail\": \"待办 p-0e693a34e02f8589 已生成;人执行 daedalus-tx approve p-0e693a34e02f8589 --expect-sha256 df1b185c6ef3c73114119a1fb09d936027eacfe61e2f68cde5005bf40667e07b 后重新调用本工具\"\n}"
      }
    ],
    "isError": true
  }
}
- human 档 apply → error=human_approval_required tier=human pending_id=p-0e693a34e02f8589
- ✅ 场景3:判为需要人批准
- ✅ 场景3:未批准不执行
- ✅ 场景3:待办已落盘
- 待办 argv 指纹: df1b185c6ef3c73114119a1fb09d936027eacfe61e2f68cde5005bf40667e07b

`$ sudo -i /home/udefine-dev/tx-e2e-v2/bin/daedalus-tx approve p-0e693a34e02f8589 --expect-sha256 df1b185c6ef3c73114119a1fb09d936027eacfe61e2f68cde5005bf40667e07b `

```
{"id":"p-0e693a34e02f8589","path":"/run/daedalus/approvals/p-0e693a34e02f8589.json","argv":["systemctl","--user","restart","daedalus-tx-e2e-human.service"],"argv_sha256":"df1b185c6ef3c73114119a1fb09d936027eacfe61e2f68cde5005bf40667e07b","approved_by":"root","approved_at":"2026-11-21T14:42:22Z"}
```
退出码: 0
- ✅ 场景3:批准记录已落盘
-rw-r--r--. 1 root root 167 Nov 21 22:42 /run/daedalus/approvals/p-0e693a34e02f8589.json
- 批准后 apply → outcome=applied
- ✅ 场景3:批准后执行

## 场景 3b(待办带人):两个用户对同一意图不得撞同一个待办 id
tx_apply(daedalus-e2e-other, human 档) 原始: {
  "jsonrpc": "2.0",
  "id": 2,
  "result": {
    "content": [
      {
        "type": "text",
        "text": "{\n  \"error\": \"human_approval_required\",\n  \"tier\": \"human\",\n  \"pending_id\": \"p-5087acfd07aad07f\",\n  \"argv\": [\n    \"systemctl\",\n    \"--user\",\n    \"restart\",\n    \"daedalus-tx-e2e-human.service\"\n  ],\n  \"argv_sha256\": \"df1b185c6ef3c73114119a1fb09d936027eacfe61e2f68cde5005bf40667e07b\",\n  \"reason\": \"需要人批准\",\n  \"detail\": \"待办 p-5087acfd07aad07f 已生成;人执行 daedalus-tx approve p-5087acfd07aad07f --expect-sha256 df1b185c6ef3c73114119a1fb09d936027eacfe61e2f68cde5005bf40667e07b 后重新调用本工具\"\n}"
      }
    ],
    "isError": true
  }
}
- 第二用户的待办 id = p-5087acfd07aad07f;本用户的待办 id = p-0e693a34e02f8589
- ✅ 场景3b:两个用户的待办 id 不撞

## 场景 4(危险名单):命中 policy.toml 的 high_risk_services ⇒ 至少第三档
- 危险单元 preview → tier=human
- ✅ 场景4:危险名单降档(至少第三档)

## 场景 5(拒绝信息可区分):四类拒绝各给不同理由
- 危险单元 apply → error=human_approval_required
- ✅ 场景5:拒绝带可区分码

## 清理

## 汇总
- 通过 32,失败 0

证据文件: /home/udefine-dev/tx-e2e-v2/EVIDENCE.md
