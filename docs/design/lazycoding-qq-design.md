# lazycoding QQ 频道支持设计方案

> 参考：[sipeed/picoclaw](https://github.com/sipeed/picoclaw) QQ channel 实现

---

## 一、背景与目标

lazycoding 目前通过 Telegram Bot API 将 Claude Code 暴露给远程用户，具备流式输出、消息队列、内联取消按钮、语音输入、会话持久化等核心能力。

本方案旨在以最小改动为 lazycoding 增加 **QQ 频道支持**，使用户可以通过 QQ 与 Claude Code 交互，同时复用现有的 Claude CLI 调用逻辑、会话管理和消息队列机制。

---

## 二、QQ Bot 接入方式选型

### 2.1 QQ 开放平台官方 API（推荐）

QQ 开放平台（[bot.q.qq.com](https://bot.q.qq.com)）提供官方 Bot API v2，支持：

- **Stream Mode（长连接 WebSocket）**：无需公网 IP，bot 主动连接到 QQ 服务器接收事件，与 picoclaw、nanobot 的 DingTalk 接入方式类似
- **Webhook Mode**：QQ 服务器推送事件到用户服务器（需公网 IP 和 HTTPS）

**推荐使用 Stream Mode**，原因：
- 与 lazycoding 的部署场景（开发者本机/内网服务器）匹配
- 无需暴露端口，配置简单
- picoclaw 和 nanobot 的 QQ 实现均采用此方式

### 2.2 认证机制

```
AppID + AppSecret → POST /app/getAppAccessToken → access_token（有效期 7200s）
```

Token 需要在过期前自动刷新，picoclaw 的实现中有 ticker 定时刷新逻辑可参考。

### 2.3 消息类型支持

QQ Bot 官方 API 支持的事件类型：

| 事件类型 | 说明 |
|---------|------|
| `C2C_MESSAGE_CREATE` | 用户私聊 Bot |
| `GROUP_AT_MESSAGE_CREATE` | 群内 @Bot 消息 |
| `AT_MESSAGE_CREATE` | 频道内 @Bot 消息 |
| `DIRECT_MESSAGE_CREATE` | 频道私信 |

**初期只需支持 `C2C_MESSAGE_CREATE`**（私聊），与 Telegram 私聊体验一致，后续可扩展群组支持。

---

## 三、lazycoding 现有架构回顾

```
config.yaml
    └── 多项目映射 (conversation_id → work_dir)

main.go
    ├── telegram.go     ← Bot 入口，轮询 + 处理 Update
    ├── claude.go       ← 调用 claude --print --output-format stream-json
    ├── session.go      ← 会话持久化到 ~/.lazycoding/sessions.json
    ├── queue.go        ← 消息队列（防并发）
    └── voice.go        ← 语音转文字（Groq/Whisper）
```

关键接口抽象（现有代码中隐含）：

```go
type Message struct {
    UserID  string
    Text    string
    WorkDir string
}

type Reply struct {
    ChatID  string // Telegram chat_id 或 QQ openid
    Text    string
}
```

---

## 四、新增 QQ Channel 设计

### 4.1 整体架构

```
config.yaml
    ├── telegram: { token, ... }
    └── qq: { app_id, app_secret, allow_from, work_dir }  ← 新增

main.go
    ├── telegram.go         ← 不变
    ├── qq.go               ← 新增，QQ channel 入口
    │   ├── qq_auth.go      ← access_token 管理
    │   ├── qq_ws.go        ← WebSocket Stream Mode 连接
    │   └── qq_api.go       ← 发消息 REST API
    ├── claude.go           ← 复用，不变
    ├── session.go          ← 复用，不变（session key 改用 user_openid）
    ├── queue.go            ← 复用，不变
    └── voice.go            ← 复用（QQ 语音消息为 silk 格式，需转换）
```

### 4.2 配置文件扩展

```yaml
# config.yaml

telegram:
  token: "xxx"
  allowed_users: []

qq:
  enabled: true
  app_id: "YOUR_APP_ID"
  app_secret: "YOUR_APP_SECRET"
  allow_from: []          # 空表示允许所有人，填 openid 则白名单限制
  sandbox: true           # 沙箱模式（个人开发者使用）
  work_dir: "~/projects/myapp"   # 默认工作目录
  # 多对话映射（可选，与 Telegram 的 conversation→dir 逻辑一致）
  sessions:
    - openid: "xxx"
      work_dir: "~/projects/project-a"
```

### 4.3 QQ WebSocket Stream Mode 实现

```go
// qq_ws.go

package main

import (
    "encoding/json"
    "log"
    "time"
    "github.com/gorilla/websocket"
)

const (
    QQGatewayURL    = "wss://api.sgroup.qq.com/websocket"
    QQSandboxGWURL  = "wss://sandbox.api.sgroup.qq.com/websocket"
    OpDispatch      = 0   // 事件分发
    OpHeartbeat     = 1   // 心跳
    OpIdentify      = 2   // 鉴权
    OpReconnect     = 7   // 要求重连
    OpInvalidSession= 9   // 非法 session
    OpHello         = 10  // 连接成功
    OpHeartbeatAck  = 11  // 心跳 ACK
    IntentC2C       = 1 << 25  // 私信事件
)

type QQChannel struct {
    cfg       QQConfig
    token     *TokenManager
    conn      *websocket.Conn
    sessionID string
    seq       int64
    handler   func(openid, text string)  // 收到消息后的回调
}

func (q *QQChannel) Start() error {
    if err := q.token.Refresh(); err != nil {
        return err
    }
    go q.token.AutoRefresh()  // 每 7000s 刷新一次

    return q.connect()
}

func (q *QQChannel) connect() error {
    gwURL := QQGatewayURL
    if q.cfg.Sandbox {
        gwURL = QQSandboxGWURL
    }

    conn, _, err := websocket.DefaultDialer.Dial(gwURL, nil)
    if err != nil {
        return err
    }
    q.conn = conn
    go q.readLoop()
    return nil
}

func (q *QQChannel) readLoop() {
    for {
        _, raw, err := q.conn.ReadMessage()
        if err != nil {
            log.Printf("[QQ] WS read error: %v, reconnecting...", err)
            time.Sleep(5 * time.Second)
            q.connect()
            return
        }
        q.handlePayload(raw)
    }
}

func (q *QQChannel) handlePayload(raw []byte) {
    var p Payload
    json.Unmarshal(raw, &p)

    switch p.Op {
    case OpHello:
        // 开始心跳
        interval := p.D["heartbeat_interval"].(float64)
        go q.heartbeat(time.Duration(interval) * time.Millisecond)
        // 发送 Identify
        q.identify()

    case OpDispatch:
        q.seq = p.S
        if p.T == "C2C_MESSAGE_CREATE" {
            q.onC2CMessage(p.D)
        }
        // 后续可扩展 GROUP_AT_MESSAGE_CREATE

    case OpReconnect:
        q.conn.Close()
        time.Sleep(2 * time.Second)
        q.connect()

    case OpInvalidSession:
        q.sessionID = ""
        time.Sleep(2 * time.Second)
        q.identify()
    }
}

func (q *QQChannel) identify() {
    payload := map[string]interface{}{
        "op": OpIdentify,
        "d": map[string]interface{}{
            "token":   "QQBot " + q.token.Get(),
            "intents": IntentC2C,
            "shard":   []int{0, 1},
        },
    }
    q.conn.WriteJSON(payload)
}

func (q *QQChannel) heartbeat(interval time.Duration) {
    ticker := time.NewTicker(interval)
    defer ticker.Stop()
    for range ticker.C {
        q.conn.WriteJSON(map[string]interface{}{
            "op": OpHeartbeat,
            "d":  q.seq,
        })
    }
}
```

### 4.4 消息接收与分发

```go
func (q *QQChannel) onC2CMessage(d map[string]interface{}) {
    author := d["author"].(map[string]interface{})
    openid := author["user_openid"].(string)
    text   := d["content"].(string)
    msgID  := d["id"].(string)

    // 白名单检查
    if !q.cfg.isAllowed(openid) {
        return
    }

    // 去除 @Bot 前缀（群消息时存在）
    text = strings.TrimSpace(text)

    // 交给通用处理逻辑（复用现有 queue + claude 调用）
    q.handler(openid, text, msgID)
}
```

### 4.5 消息发送

```go
// qq_api.go

func (q *QQChannel) SendMessage(openid, content, msgID string) error {
    // QQ 限制：每条消息必须回复在 5 条以内，超出需使用主动消息（需审核）
    // 流式输出需分段发送

    url := fmt.Sprintf("https://api.sgroup.qq.com/v2/users/%s/messages", openid)
    if q.cfg.Sandbox {
        url = fmt.Sprintf("https://sandbox.api.sgroup.qq.com/v2/users/%s/messages", openid)
    }

    body := map[string]interface{}{
        "content":  content,
        "msg_type": 0,  // 文本消息
        "msg_id":   msgID,  // 被动回复需要携带
        "msg_seq":  q.nextSeq(),  // 防重
    }

    req, _ := http.NewRequest("POST", url, jsonBody(body))
    req.Header.Set("Authorization", "QQBot "+q.token.Get())
    req.Header.Set("Content-Type", "application/json")

    resp, err := http.DefaultClient.Do(req)
    // ... 处理响应
    return err
}
```

### 4.6 流式输出适配

lazycoding 的流式输出核心是将 `stream-json` 格式的行实时追加后发送给用户。Telegram 的做法是编辑同一条消息（`editMessageText`）。

QQ Bot API 不支持编辑已发送消息，需要改为**分段发送**策略：

```
策略 A（简单）：缓冲至换行/标点，每段单独发一条消息
策略 B（推荐）：缓冲 1.5s 或 500 字，发一条；保留 msg_id 链式回复
策略 C：完整输出后一次性发送（丧失流式体验，不推荐）
```

**推荐策略 B**，实现：

```go
func (q *QQChannel) streamReply(openid, firstMsgID string, stream <-chan string) {
    var buf strings.Builder
    ticker := time.NewTicker(1500 * time.Millisecond)
    defer ticker.Stop()

    flush := func() {
        if buf.Len() == 0 {
            return
        }
        q.SendMessage(openid, buf.String(), firstMsgID)
        buf.Reset()
    }

    for {
        select {
        case chunk, ok := <-stream:
            if !ok {
                flush()  // 最终输出
                return
            }
            buf.WriteString(chunk)
            if buf.Len() > 500 {
                flush()
            }
        case <-ticker.C:
            flush()
        }
    }
}
```

### 4.7 命令支持

QQ Bot 不支持 Telegram 的 inline keyboard，改为文本命令：

| 命令 | 功能 |
|-----|------|
| `/workdir <path>` | 切换工作目录 |
| `/session` | 查看当前会话信息 |
| `/cancel` | 取消当前任务 |
| `/reset` | 清除会话历史 |
| `/help` | 显示帮助 |

取消（Cancel）按钮功能退化为发送 `/cancel` 文字命令，逻辑上调用现有的 `CancelCurrentTask()` 即可。

### 4.8 限制与注意事项

**QQ Bot 官方 API 的限制**（需要在代码注释和文档中标注）：

1. **被动消息**：Bot 只能回复用户在 5 分钟内发送的消息（携带 `msg_id`），超时后消息作废
2. **URL 过滤**：QQ 会过滤消息中大多数 URL（包括 GitHub 链接），Claude Code 输出的路径引用可能被屏蔽
3. **5 条回复限制**：每条用户消息最多发 5 条被动回复，超出需申请主动消息权限
4. **Markdown 不渲染**：QQ 不支持 Markdown 格式，需要过滤或转换（`**bold**` → 纯文本）
5. **沙箱限制**：个人开发者只能在沙箱中使用，正式发布需要企业资质审核

---

## 五、文件结构变更

```
lazycoding/
├── main.go              ← 新增 QQ channel 启动逻辑
├── telegram.go          ← 不变
├── qq/
│   ├── channel.go       ← QQChannel 主体，实现 Channel 接口
│   ├── auth.go          ← TokenManager（access_token 刷新）
│   ├── ws.go            ← WebSocket Stream Mode
│   └── api.go           ← REST API（发消息、获取用户信息）
├── claude.go            ← 不变
├── session.go           ← 小调整：session key 支持 "qq:{openid}" 前缀
├── queue.go             ← 不变
├── voice.go             ← 可选：QQ silk → pcm 转换
└── config.yaml          ← 新增 qq 配置节
```

### Channel 接口抽象（推荐引入）

如果将来还要支持微信、Discord 等，建议同时引入 Channel 接口：

```go
type Channel interface {
    Start() error
    Stop()
    Send(userID, text string) error
    Name() string
}
```

`telegram.go` 和 `qq/channel.go` 都实现此接口，`main.go` 统一启动所有启用的 channel。

---

## 六、实现步骤

**Phase 1（核心 MVP）**

1. `qq/auth.go`：实现 `TokenManager`，支持 AppID/AppSecret → access_token 获取和自动刷新
2. `qq/ws.go`：实现 WebSocket 长连接，处理 Hello/Heartbeat/Identify/Dispatch 流程
3. `qq/api.go`：实现 `SendMessage` REST 调用
4. `qq/channel.go`：串联以上，接收 `C2C_MESSAGE_CREATE` → 调用 Claude → 分段回复
5. `config.yaml`：添加 `qq` 配置节
6. `main.go`：根据配置决定是否启动 QQ channel

**Phase 2（体验优化）**

7. 流式分段发送策略（策略 B）
8. Markdown 过滤器（去除 `**`、`#` 等符号）
9. 消息队列接入（复用现有 `queue.go`）
10. 会话持久化（session key 加 `qq:` 前缀区分来源）

**Phase 3（可选扩展）**

11. 群组 `GROUP_AT_MESSAGE_CREATE` 支持（需 @Bot 触发）
12. 语音消息支持（silk 格式解码 → Whisper 转文字）
13. 图片接收（QQ 返回图片 URL，需下载后作为附件传给 Claude）

---

## 七、依赖

只需新增一个 WebSocket 库（picoclaw 使用的也是同款）：

```go
// go.mod 新增
require (
    github.com/gorilla/websocket v1.5.3
)
```

其余 HTTP 调用使用标准库 `net/http`，无额外依赖，保持 lazycoding 轻量特性。

---

## 八、参考资源

- [QQ 开放平台文档](https://bot.q.qq.com/wiki/)
- [picoclaw QQ channel 实现（PR #5）](https://github.com/sipeed/picoclaw/pull/5) — 可直接参考 Go 实现
- [nanobot QQ channel](https://github.com/HKUDS/nanobot) — 另一个 Go 参考实现
- [QQ Bot API v2 Stream Mode 文档](https://bot.q.qq.com/wiki/develop/api-v2/dev-prepare/interface-framework/event-emit.html)
