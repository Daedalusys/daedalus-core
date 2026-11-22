# tx 通道真机 E2E 证据(v3,2026-11-22)

第三轮:第三档人工闸门的归属显式化 —— 待办按人分目录、批准记录写请求者并被消费侧
显式核对、一份批准只能用一次,外加第二个**真用户会话**(家目录 + login shell +
linger 起的 user manager)。

- 目标机:`Masterwet_Server`(hostname `undef-dev`,x86_64,systemd 259,`udefine-dev`
  uid 1000 在活着的用户会话里,sudo 免密)
- 二进制:core CLI 从 `feat/tx-core-v3` 工作树构建(`cmd/daedalus-tx`);MCP 插件从
  `tx-plugin-v3/tx` 工作树构建(`cmd/daedalus-tx-mcp`)
- 脚本:`tests/mcp/tx-e2e.sh`;确定性客户端:`tests/mcp/tx-e2e-client.py`
- 档位表与测试单元由脚本自装自清(跑完 `/etc/daedalus/` 为空,临时用户连同家目录删掉)

下面是脚本产物的**原文**(每条命令、它的输出与退出码),不是结论转述。


## preflight:环境能力
- uid=1000 user=udefine-dev systemd=systemd 259 (259.10-1.fc44)
- systemctl --user 可用(有活着的用户会话)
- ✅ preflight:systemctl --user
- sudo 免密可用

## 准备:/run/daedalus 目录与签发者组

## 准备:第二个用户(真用户 + 真会话)
- 第二用户: daedalus-e2e-other(uid=1003 home=/home/daedalus-e2e-other)

`$ sudo loginctl enable-linger daedalus-e2e-other `

```

```
退出码: 0
- ✅ 准备:第二用户的 user bus 可用
- ✅ 准备:第二用户 Linger=yes
drwxr-xr-x. 6 root root             120 Nov 22 15:01 /run/daedalus
drwxr-xr-x. 2 root root              40 Nov 22 15:09 /run/daedalus/approvals
drwxrwxrwt. 2 root root              40 Nov 22 15:09 /run/daedalus/consumed
drwxrwxr-t. 2 root daedalus-signers  40 Nov 22 15:09 /run/daedalus/grants
drwxrwxrwt. 2 root root              40 Nov 22 15:09 /run/daedalus/pending

## 准备:档位表 /etc/daedalus/agent-autonomy.toml
- 档位表已装: auto=daedalus-tx-e2e-auto.service / confirm=daedalus-tx-e2e-confirm.service / human=daedalus-tx-e2e-human.service

## 准备:用户域测试单元(两个用户各自的 user 域)
UNIT FILE                       STATE   PRESET
daedalus-tx-e2e-auto.service    enabled disabled
daedalus-tx-e2e-confirm.service enabled disabled
daedalus-tx-e2e-human.service   enabled disabled

3 unit files listed.
UNIT FILE                       STATE   PRESET
daedalus-tx-e2e-confirm.service enabled disabled
daedalus-tx-e2e-human.service   enabled disabled

2 unit files listed.

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
{"id":"g-20261122-44b8dd87","path":"/run/daedalus/grants/g-20261122-44b8dd87.json","scope":["systemd:user:restart:daedalus-tx-e2e-confirm.service"],"issued_by":"udefine-dev","issued_at":"2026-11-22T07:09:09Z","expires_at":"2026-11-22T07:19:09Z","max_uses":1}
```
退出码: 0
- 条子 id = g-20261122-44b8dd87
- 条子文件: /run/daedalus/grants/g-20261122-44b8dd87.json
-rw-r--r--. 1 udefine-dev daedalus-signers 214 Nov 22 15:09 /run/daedalus/grants/g-20261122-44b8dd87.json
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
        "text": "{\n  \"tx_id\": \"698af147bd3b0c65\",\n  \"tier\": \"confirm\",\n  \"outcome\": \"applied\",\n  \"grant_id\": \"g-20261122-44b8dd87\",\n  \"argv\": [\n    \"systemctl\",\n    \"--user\",\n    \"restart\",\n    \"daedalus-tx-e2e-confirm.service\"\n  ],\n  \"scope\": \"systemd:user:restart:daedalus-tx-e2e-confirm.service\"\n}"
      }
    ]
  }
}
- 账本在系统路径 apply → outcome=applied grant_id=g-20261122-44b8dd87
- ✅ 场景2b:有匹配条子放行

```
{"id":"g-20261122-180ffa56","path":"/run/daedalus/grants/g-20261122-180ffa56.json","scope":["systemd:user:restart:daedalus-tx-e2e-confirm.service"],"issued_by":"root","issued_at":"2026-11-22T07:09:09Z","expires_at":"2026-11-22T07:19:09Z","max_uses":1,"for_uid":1000,"for_user":"udefine-dev"}
```
退出码: 0
- ✅ 场景2c:root 签 --for 成功
- ✅ 场景2c:root 签的条子属主=root
- ✅ 场景2c:for_uid 指向本用户

`$ sudo env -u SUDO_USER /home/udefine-dev/tx-e2e-v3/bin/daedalus-tx grant add --scope systemd:user:restart:daedalus-tx-e2e-confirm.service --ttl 10m --count 1 `

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
        "text": "{\n  \"tx_id\": \"bcc6c159323e2373\",\n  \"tier\": \"confirm\",\n  \"outcome\": \"applied\",\n  \"grant_id\": \"g-20261122-180ffa56\",\n  \"argv\": [\n    \"systemctl\",\n    \"--user\",\n    \"restart\",\n    \"daedalus-tx-e2e-confirm.service\"\n  ],\n  \"scope\": \"systemd:user:restart:daedalus-tx-e2e-confirm.service\"\n}"
      }
    ]
  }
}
- ✅ 场景2c:root 给本用户签的条子放行

## 场景 2d(绑定):给别人签的条子本用户用不了 —— 真机越权的那条路径

```
{"id":"g-20261122-2809f4ff","path":"/run/daedalus/grants/g-20261122-2809f4ff.json","scope":["systemd:user:restart:daedalus-tx-e2e-confirm.service"],"issued_by":"root","issued_at":"2026-11-22T07:09:10Z","expires_at":"2026-11-22T07:19:10Z","max_uses":1,"for_uid":1003,"for_user":"daedalus-e2e-other"}
```
退出码: 0
- 给 daedalus-e2e-other(uid=1003)签的条子 id = g-20261122-2809f4ff
R2X 原始: {
  "jsonrpc": "2.0",
  "id": 2,
  "result": {
    "content": [
      {
        "type": "text",
        "text": "{\n  \"error\": \"grant_for_other_user\",\n  \"tier\": \"confirm\",\n  \"available_grants\": [\n    {\n      \"id\": \"g-20261122-180ffa56\",\n      \"scope\": [\n        \"systemd:user:restart:daedalus-tx-e2e-confirm.service\"\n      ],\n      \"expires_at\": \"2026-11-22T07:19:09Z\",\n      \"max_uses\": 1,\n      \"used\": 1,\n      \"remaining\": 0,\n      \"expired\": false\n    },\n    {\n      \"id\": \"g-20261122-44b8dd87\",\n      \"scope\": [\n        \"systemd:user:restart:daedalus-tx-e2e-confirm.service\"\n      ],\n      \"expires_at\": \"2026-11-22T07:19:09Z\",\n      \"max_uses\": 1,\n      \"used\": 1,\n      \"remaining\": 0,\n      \"expired\": false\n    }\n  ],\n  \"reason\": \"条子不是给你的\",\n  \"detail\": \"grant: 这张条子是给 \\\"daedalus-e2e-other\\\"(uid=1003)的,不是给 \\\"udefine-dev\\\"(uid=1000)的\"\n}"
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
        "text": "{\n  \"executor\": {\n    \"uid\": 1000,\n    \"name\": \"udefine-dev\"\n  },\n  \"grants\": [\n    {\n      \"id\": \"g-20261122-180ffa56\",\n      \"scope\": [\n        \"systemd:user:restart:daedalus-tx-e2e-confirm.service\"\n      ],\n      \"expires_at\": \"2026-11-22T07:19:09Z\",\n      \"max_uses\": 1,\n      \"used\": 1,\n      \"remaining\": 0,\n      \"expired\": false\n    },\n    {\n      \"id\": \"g-20261122-44b8dd87\",\n      \"scope\": [\n        \"systemd:user:restart:daedalus-tx-e2e-confirm.service\"\n      ],\n      \"expires_at\": \"2026-11-22T07:19:09Z\",\n      \"max_uses\": 1,\n      \"used\": 1,\n      \"remaining\": 0,\n      \"expired\": false\n    }\n  ],\n  \"invalid\": []\n}"
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
        "text": "{\n  \"executor\": {\n    \"uid\": 1003,\n    \"name\": \"daedalus-e2e-other\"\n  },\n  \"grants\": [\n    {\n      \"id\": \"g-20261122-2809f4ff\",\n      \"scope\": [\n        \"systemd:user:restart:daedalus-tx-e2e-confirm.service\"\n      ],\n      \"expires_at\": \"2026-11-22T07:19:10Z\",\n      \"max_uses\": 1,\n      \"used\": 0,\n      \"remaining\": 1,\n      \"expired\": false\n    }\n  ],\n  \"invalid\": []\n}"
      }
    ]
  }
}
- ✅ 场景2f:第二个用户的执行者身份=自己
- ✅ 场景2f:第二个用户看不到本用户的条子
- ✅ 场景2f:第二个用户能看到给他签的条子

## 场景 2g(正向绑定):root 给第二用户签条子,第二用户在自己的 user 域里执行

```
{"id":"g-20261122-439e63be","path":"/run/daedalus/grants/g-20261122-439e63be.json","scope":["systemd:user:restart:daedalus-tx-e2e-confirm.service"],"issued_by":"root","issued_at":"2026-11-22T07:09:10Z","expires_at":"2026-11-22T07:19:10Z","max_uses":1,"for_uid":1003,"for_user":"daedalus-e2e-other"}
```
退出码: 0
- ✅ 场景2g:root 给第二用户签条子成功
-rw-r--r--. 1 root root 254 Nov 22 15:09 /run/daedalus/grants/g-20261122-439e63be.json
- ✅ 场景2g:条子 for_uid=第二用户 uid
- ✅ 场景2g:条子属主=root
- ✅ 场景2g:执行前第二用户域里的单元没在跑
tx_apply(daedalus-e2e-other, 条子) 原始: {
  "jsonrpc": "2.0",
  "id": 2,
  "result": {
    "content": [
      {
        "type": "text",
        "text": "{\n  \"tx_id\": \"2966ce0ed6d610e6\",\n  \"tier\": \"confirm\",\n  \"outcome\": \"applied\",\n  \"grant_id\": \"g-20261122-439e63be\",\n  \"argv\": [\n    \"systemctl\",\n    \"--user\",\n    \"restart\",\n    \"daedalus-tx-e2e-confirm.service\"\n  ],\n  \"scope\": \"systemd:user:restart:daedalus-tx-e2e-confirm.service\"\n}"
      }
    ]
  }
}
- 第二用户用条子 apply → outcome=applied grant_id=g-20261122-439e63be
- ✅ 场景2g:第二用户用给他的条子执行
- ✅ 场景2g:第二用户域里的单元真的起来了
R2H 原始: {
  "jsonrpc": "2.0",
  "id": 2,
  "result": {
    "content": [
      {
        "type": "text",
        "text": "{\n  \"error\": \"grant_for_other_user\",\n  \"tier\": \"confirm\",\n  \"available_grants\": [\n    {\n      \"id\": \"g-20261122-180ffa56\",\n      \"scope\": [\n        \"systemd:user:restart:daedalus-tx-e2e-confirm.service\"\n      ],\n      \"expires_at\": \"2026-11-22T07:19:09Z\",\n      \"max_uses\": 1,\n      \"used\": 1,\n      \"remaining\": 0,\n      \"expired\": false\n    },\n    {\n      \"id\": \"g-20261122-44b8dd87\",\n      \"scope\": [\n        \"systemd:user:restart:daedalus-tx-e2e-confirm.service\"\n      ],\n      \"expires_at\": \"2026-11-22T07:19:09Z\",\n      \"max_uses\": 1,\n      \"used\": 1,\n      \"remaining\": 0,\n      \"expired\": false\n    }\n  ],\n  \"reason\": \"条子不是给你的\",\n  \"detail\": \"grant: 这张条子是给 \\\"daedalus-e2e-other\\\"(uid=1003)的,不是给 \\\"udefine-dev\\\"(uid=1000)的\"\n}"
      }
    ],
    "isError": true
  }
}
- ✅ 场景2g:同一张条子本用户用不了

## 场景 3(第三档 human):出待办 → 无批准不执行 → 人批准 → 执行一次
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

`$ sudo -i /home/udefine-dev/tx-e2e-v3/bin/daedalus-tx approve p-0e693a34e02f8589 --expect-sha256 df1b185c6ef3c73114119a1fb09d936027eacfe61e2f68cde5005bf40667e07b `

```
{"id":"p-0e693a34e02f8589","path":"/run/daedalus/approvals/p-0e693a34e02f8589.json","argv":["systemctl","--user","restart","daedalus-tx-e2e-human.service"],"argv_sha256":"df1b185c6ef3c73114119a1fb09d936027eacfe61e2f68cde5005bf40667e07b","approved_by":"root","approved_at":"2026-11-22T07:09:11Z"}
```
退出码: 0
- ✅ 场景3:批准记录已落盘
-rw-r--r--. 1 root root 220 Nov 22 15:09 /run/daedalus/approvals/p-0e693a34e02f8589.json
- 批准后 apply → outcome=applied
- ✅ 场景3:批准后执行
- ✅ 场景3:消费标记已落盘
-rw-------. 1 udefine-dev daedalus-signers 0 Nov 22 15:09 /run/daedalus/consumed/1000/p-0e693a34e02f8589
R3A 原始: {
  "jsonrpc": "2.0",
  "id": 2,
  "result": {
    "content": [
      {
        "type": "text",
        "text": "{\n  \"error\": \"approval_consumed\",\n  \"tier\": \"human\",\n  \"reason\": \"这份批准已经用过了,需要人重新批准\",\n  \"detail\": \"这份批准已经用过了(待办 p-0e693a34e02f8589 的批准)\"\n}"
      }
    ],
    "isError": true
  }
}
- 同一份批准第二次使用 → error=approval_consumed
- ✅ 场景3:同一份批准第二次使用被拒

## 场景 3b(待办带人):两个用户对同一意图不得撞同一个待办 id
tx_apply(daedalus-e2e-other, human 档) 原始: {
  "jsonrpc": "2.0",
  "id": 2,
  "result": {
    "content": [
      {
        "type": "text",
        "text": "{\n  \"error\": \"human_approval_required\",\n  \"tier\": \"human\",\n  \"pending_id\": \"p-624fede7d2379663\",\n  \"argv\": [\n    \"systemctl\",\n    \"--user\",\n    \"restart\",\n    \"daedalus-tx-e2e-human.service\"\n  ],\n  \"argv_sha256\": \"df1b185c6ef3c73114119a1fb09d936027eacfe61e2f68cde5005bf40667e07b\",\n  \"reason\": \"需要人批准\",\n  \"detail\": \"待办 p-624fede7d2379663 已生成;人执行 daedalus-tx approve p-624fede7d2379663 --expect-sha256 df1b185c6ef3c73114119a1fb09d936027eacfe61e2f68cde5005bf40667e07b 后重新调用本工具\"\n}"
      }
    ],
    "isError": true
  }
}
- 第二用户的待办 id = p-624fede7d2379663;本用户的待办 id = p-0e693a34e02f8589
- ✅ 场景3b:两个用户的待办 id 不撞

## 场景 3c(归属):待办按人分目录、批准记录带请求者、清单按归属显示
- ✅ 场景3c:第二用户待办落在自己的 uid 子目录
-rw-r--r--. 1 daedalus-e2e-other daedalus-e2e-other 351 Nov 22 15:09 /run/daedalus/pending/1003/p-624fede7d2379663.json
- ✅ 场景3c:本用户待办落在自己的 uid 子目录
-rw-r--r--. 1 udefine-dev daedalus-signers 351 Nov 22 15:09 /run/daedalus/pending/1000/p-0e693a34e02f8589.json
- ✅ 场景3c:待办不再落在待办根目录
- ✅ 场景3c:本用户待办目录 0700 且属主=本人
- ✅ 场景3c:第二用户待办目录 0700 且属主=本人

```
{"pending":[{"id":"p-0e693a34e02f8589","kind":"service","name":"daedalus-tx-e2e-human.service","desired_state":"restarted","argv":["systemctl","--user","restart","daedalus-tx-e2e-human.service"],"argv_sha256":"df1b185c6ef3c73114119a1fb09d936027eacfe61e2f68cde5005bf40667e07b","scope":"systemd:user:restart:daedalus-tx-e2e-human.service","reason":"tier=human (user-rule)","created_at":"2026-11-22T07:09:11Z","expires_at":"2026-11-22T08:09:11Z","expired":false,"requested_by_uid":1000,"requested_by":"udefine-dev"},{"id":"p-624fede7d2379663","kind":"service","name":"daedalus-tx-e2e-human.service","desired_state":"restarted","argv":["systemctl","--user","restart","daedalus-tx-e2e-human.service"],"argv_sha256":"df1b185c6ef3c73114119a1fb09d936027eacfe61e2f68cde5005bf40667e07b","scope":"systemd:user:restart:daedalus-tx-e2e-human.service","reason":"tier=human (user-rule)","created_at":"2026-11-22T07:09:11Z","expires_at":"2026-11-22T08:09:11Z","expired":false,"requested_by_uid":1003,"requested_by":"daedalus-e2e-other"}],"invalid":[]}
```
退出码: 0
- ✅ 场景3c:root pending list 退出码 0
- pending list 里的 id: p-0e693a34e02f8589 p-624fede7d2379663
- ✅ 场景3c:清单里有本用户的待办
- ✅ 场景3c:清单里有第二用户的待办
- ✅ 场景3c:清单里本用户待办的 requested_by
- ✅ 场景3c:清单里本用户待办的 requested_by_uid
- ✅ 场景3c:清单里第二用户待办的 requested_by
- ✅ 场景3c:清单里第二用户待办的 requested_by_uid

`$ sudo -i /home/udefine-dev/tx-e2e-v3/bin/daedalus-tx approve p-624fede7d2379663 --expect-sha256 df1b185c6ef3c73114119a1fb09d936027eacfe61e2f68cde5005bf40667e07b `

```
{"id":"p-624fede7d2379663","path":"/run/daedalus/approvals/p-624fede7d2379663.json","argv":["systemctl","--user","restart","daedalus-tx-e2e-human.service"],"argv_sha256":"df1b185c6ef3c73114119a1fb09d936027eacfe61e2f68cde5005bf40667e07b","approved_by":"root","approved_at":"2026-11-22T07:09:12Z"}
```
退出码: 0
-rw-r--r--. 1 root root 227 Nov 22 15:09 /run/daedalus/approvals/p-624fede7d2379663.json
- ✅ 场景3c:批准记录 requested_by_uid=第二用户
- ✅ 场景3c:批准记录 requested_by=第二用户账号名
tx_pending(daedalus-e2e-other) 原始: {
  "jsonrpc": "2.0",
  "id": 2,
  "result": {
    "content": [
      {
        "type": "text",
        "text": "{\n  \"items\": [\n    {\n      \"id\": \"p-624fede7d2379663\",\n      \"kind\": \"service\",\n      \"name\": \"daedalus-tx-e2e-human.service\",\n      \"desired_state\": \"restarted\",\n      \"scope\": \"systemd:user:restart:daedalus-tx-e2e-human.service\",\n      \"argv\": [\n        \"systemctl\",\n        \"--user\",\n        \"restart\",\n        \"daedalus-tx-e2e-human.service\"\n      ],\n      \"argv_sha256\": \"df1b185c6ef3c73114119a1fb09d936027eacfe61e2f68cde5005bf40667e07b\",\n      \"reason\": \"tier=human (user-rule)\",\n      \"created_at\": \"2026-11-22T07:09:11Z\",\n      \"expires_at\": \"2026-11-22T08:09:11Z\"\n    }\n  ]\n}"
      }
    ]
  }
}
- ✅ 场景3c:第二用户 tx_pending 里有自己的待办
- ✅ 场景3c:第二用户 tx_pending 不含本用户的待办
tx_pending(本用户) 原始: {
  "jsonrpc": "2.0",
  "id": 2,
  "result": {
    "content": [
      {
        "type": "text",
        "text": "{\n  \"items\": [\n    {\n      \"id\": \"p-0e693a34e02f8589\",\n      \"kind\": \"service\",\n      \"name\": \"daedalus-tx-e2e-human.service\",\n      \"desired_state\": \"restarted\",\n      \"scope\": \"systemd:user:restart:daedalus-tx-e2e-human.service\",\n      \"argv\": [\n        \"systemctl\",\n        \"--user\",\n        \"restart\",\n        \"daedalus-tx-e2e-human.service\"\n      ],\n      \"argv_sha256\": \"df1b185c6ef3c73114119a1fb09d936027eacfe61e2f68cde5005bf40667e07b\",\n      \"reason\": \"tier=human (user-rule)\",\n      \"created_at\": \"2026-11-22T07:09:11Z\",\n      \"expires_at\": \"2026-11-22T08:09:11Z\"\n    }\n  ]\n}"
      }
    ]
  }
}
- ✅ 场景3c:本用户 tx_pending 里有自己的待办
- ✅ 场景3c:本用户 tx_pending 不含第二用户的待办

## 场景 3e(正向):第二用户用「批给他的」那份批准执行
tx_apply(daedalus-e2e-other, 已批准) 原始: {
  "jsonrpc": "2.0",
  "id": 2,
  "result": {
    "content": [
      {
        "type": "text",
        "text": "{\n  \"tx_id\": \"b9a6c202d01bd276\",\n  \"tier\": \"human\",\n  \"outcome\": \"applied\",\n  \"pending_id\": \"p-624fede7d2379663\",\n  \"argv\": [\n    \"systemctl\",\n    \"--user\",\n    \"restart\",\n    \"daedalus-tx-e2e-human.service\"\n  ],\n  \"scope\": \"systemd:user:restart:daedalus-tx-e2e-human.service\",\n  \"approved_by\": \"root\"\n}"
      }
    ]
  }
}
- 第二用户已批准 apply → outcome=applied approved_by=root
- ✅ 场景3e:第二用户用被批准的待办执行
- ✅ 场景3e:第二用户域里的单元真的动了

## 场景 3d(参数变了):人批准的是被改写过的 argv,agent 执行时指纹对不上
tx_apply(stopped) 原始: {
  "jsonrpc": "2.0",
  "id": 2,
  "result": {
    "content": [
      {
        "type": "text",
        "text": "{\n  \"error\": \"human_approval_required\",\n  \"tier\": \"human\",\n  \"pending_id\": \"p-1e367e9dd5704c8f\",\n  \"argv\": [\n    \"systemctl\",\n    \"--user\",\n    \"stop\",\n    \"daedalus-tx-e2e-human.service\"\n  ],\n  \"argv_sha256\": \"ac041ea77677ee8e4a48fc35ea2f35aa544285c123465f936e224dcd54a84734\",\n  \"reason\": \"需要人批准\",\n  \"detail\": \"待办 p-1e367e9dd5704c8f 已生成;人执行 daedalus-tx approve p-1e367e9dd5704c8f --expect-sha256 ac041ea77677ee8e4a48fc35ea2f35aa544285c123465f936e224dcd54a84734 后重新调用本工具\"\n}"
      }
    ],
    "isError": true
  }
}
- 新待办 id = p-1e367e9dd5704c8f 文件 = /run/daedalus/pending/1000/p-1e367e9dd5704c8f.json
- ✅ 场景3d:stop 也判为第三档
- 待办 argv 已改写为 ['systemctl', '--user', 'restart', 'sshd-e2e-other.service']

```
{"id":"p-1e367e9dd5704c8f","kind":"service","name":"daedalus-tx-e2e-human.service","desired_state":"stopped","argv":["systemctl","--user","restart","sshd-e2e-other.service"],"argv_sha256":"97f4ed11058861909e4ea31a77dea2f4508a5198175e4cc1f47dbafbd2dd59fd","scope":"systemd:user:stop:daedalus-tx-e2e-human.service","reason":"tier=human (user-rule)","created_at":"2026-11-22T07:09:12Z","expires_at":"2026-11-22T08:09:12Z","expired":false,"requested_by_uid":1000,"requested_by":"udefine-dev"}
```
退出码: 0
- 人看到的指纹 = 97f4ed11058861909e4ea31a77dea2f4508a5198175e4cc1f47dbafbd2dd59fd(原始指纹 = ac041ea77677ee8e4a48fc35ea2f35aa544285c123465f936e224dcd54a84734)

`$ sudo -i /home/udefine-dev/tx-e2e-v3/bin/daedalus-tx approve p-1e367e9dd5704c8f --expect-sha256 97f4ed11058861909e4ea31a77dea2f4508a5198175e4cc1f47dbafbd2dd59fd `

```
{"id":"p-1e367e9dd5704c8f","path":"/run/daedalus/approvals/p-1e367e9dd5704c8f.json","argv":["systemctl","--user","restart","sshd-e2e-other.service"],"argv_sha256":"97f4ed11058861909e4ea31a77dea2f4508a5198175e4cc1f47dbafbd2dd59fd","approved_by":"root","approved_at":"2026-11-22T07:09:13Z"}
```
退出码: 0
agent 执行时 apply 原始: {
  "jsonrpc": "2.0",
  "id": 2,
  "result": {
    "content": [
      {
        "type": "text",
        "text": "{\n  \"error\": \"approval_mismatch\",\n  \"tier\": \"human\",\n  \"reason\": \"批准的不是这一条 argv(参数变了就是新动作)\",\n  \"detail\": \"批准记录里的 argv_sha256 与本次将执行的 argv 不一致,请重新申请待办并重新批准\"\n}"
      }
    ],
    "isError": true
  }
}
- agent 执行时 apply → error=approval_mismatch
- ✅ 场景3d:参数变了 ⇒ approval_mismatch
- ✅ 场景3d:指纹不符时不执行

`$ sudo -i /home/udefine-dev/tx-e2e-v3/bin/daedalus-tx approve p-1e367e9dd5704c8f --expect-sha256 ac041ea77677ee8e4a48fc35ea2f35aa544285c123465f936e224dcd54a84734 `

```
{"id":"p-1e367e9dd5704c8f","path":"/run/daedalus/approvals/p-1e367e9dd5704c8f.json","argv":["systemctl","--user","stop","daedalus-tx-e2e-human.service"],"argv_sha256":"ac041ea77677ee8e4a48fc35ea2f35aa544285c123465f936e224dcd54a84734","approved_by":"root","approved_at":"2026-11-22T07:09:13Z"}
```
退出码: 0
重批后 apply 原始: {
  "jsonrpc": "2.0",
  "id": 2,
  "result": {
    "content": [
      {
        "type": "text",
        "text": "{\n  \"tx_id\": \"b2bf4b60b35c7ad7\",\n  \"tier\": \"human\",\n  \"outcome\": \"applied\",\n  \"pending_id\": \"p-1e367e9dd5704c8f\",\n  \"argv\": [\n    \"systemctl\",\n    \"--user\",\n    \"stop\",\n    \"daedalus-tx-e2e-human.service\"\n  ],\n  \"scope\": \"systemd:user:stop:daedalus-tx-e2e-human.service\",\n  \"approved_by\": \"root\"\n}"
      }
    ]
  }
}
- ✅ 场景3d:按真实指纹重批后可用

## 场景 3f(伪造归属):落在受害者 id 上的待办被批准后,受害者本人不得执行
- 伪造件属主=daedalus-e2e-other

```
{"id":"p-0e693a34e02f8589","kind":"service","name":"daedalus-tx-e2e-human.service","desired_state":"restarted","argv":["systemctl","--user","restart","daedalus-tx-e2e-human.service"],"argv_sha256":"df1b185c6ef3c73114119a1fb09d936027eacfe61e2f68cde5005bf40667e07b","scope":"systemd:user:restart:daedalus-tx-e2e-human.service","reason":"tier=human (user-rule)","created_at":"2026-11-22T07:09:11Z","expires_at":"2026-11-22T08:09:11Z","expired":false,"requested_by_uid":1003,"requested_by":"daedalus-e2e-other"}
```
退出码: 0
- ✅ 场景3f:root 看到的请求者是第二用户

`$ sudo -i /home/udefine-dev/tx-e2e-v3/bin/daedalus-tx approve p-0e693a34e02f8589 --expect-sha256 df1b185c6ef3c73114119a1fb09d936027eacfe61e2f68cde5005bf40667e07b `

```
{"id":"p-0e693a34e02f8589","path":"/run/daedalus/approvals/p-0e693a34e02f8589.json","argv":["systemctl","--user","restart","daedalus-tx-e2e-human.service"],"argv_sha256":"df1b185c6ef3c73114119a1fb09d936027eacfe61e2f68cde5005bf40667e07b","approved_by":"root","approved_at":"2026-11-22T07:09:13Z"}
```
退出码: 0
受害者本人 apply 原始: {
  "jsonrpc": "2.0",
  "id": 2,
  "result": {
    "content": [
      {
        "type": "text",
        "text": "{\n  \"error\": \"approval_for_other_user\",\n  \"tier\": \"human\",\n  \"reason\": \"这份批准不是批给你的,拒绝执行\",\n  \"detail\": \"批准记录 /run/daedalus/approvals/p-0e693a34e02f8589.json 批的是 \\\"daedalus-e2e-other\\\"(uid=1003)的请求,不是你的(uid=1000),拒绝执行\"\n}"
      }
    ],
    "isError": true
  }
}
- 受害者本人 apply → error=approval_for_other_user
- ✅ 场景3f:批给别人的批准受害者用不了
- ✅ 场景3f:被拒后单元没动

## 场景 4(危险名单):命中 policy.toml 的 high_risk_services ⇒ 至少第三档
- 危险单元 preview → tier=human
- ✅ 场景4:危险名单降档(至少第三档)

## 场景 5(拒绝信息可区分):四类拒绝各给不同理由
- 危险单元 apply → error=human_approval_required
- ✅ 场景5:拒绝带可区分码

## 场景 6(每人一本账):两个用户各有一本用量账本
- ✅ 场景6:本用户的账本存在
-rw-r-----. 1 udefine-dev daedalus-signers 156 Nov 22 15:09 /var/lib/daedalus/tx/usage-1000.jsonl
- ✅ 场景6:本用户账本属主=本人
- ✅ 场景6:第二用户的账本存在
-rw-r-----. 1 daedalus-e2e-other daedalus-e2e-other 78 Nov 22 15:09 /var/lib/daedalus/tx/usage-1003.jsonl
- ✅ 场景6:第二用户账本属主=第二用户
- ✅ 场景6:第二用户账本记的是他自己的条子
- ✅ 场景6:两本账本不是同一个文件

## 清理

## 汇总
- 通过 76,失败 0

证据文件: /home/udefine-dev/tx-e2e-v3/EVIDENCE.md
