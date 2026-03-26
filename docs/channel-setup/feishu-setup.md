# 飞书 Bot 配置教程

## 前置条件

- 飞书账号（需要属于一个团队，免费创建即可）
- myclaw 已编译（`make build`）
- 公网可访问的 URL（用于 webhook，可用 cloudflared 隧道）

## 第一步：创建飞书应用

1. 登录 [飞书开放平台](https://open.feishu.cn/)
2. 点击「创建应用」→ 选择「企业自建应用」
3. 填写应用名称（如 `myclaw`）和描述
4. 进入应用 →「凭证与基础信息」，记录：
   - **App ID**（如 `cli_a5xxxxx`）
   - **App Secret**

## 第二步：添加机器人能力

1. 进入「应用能力」→「添加应用能力」
2. 选择 **机器人**

## 第三步：配置权限

进入「权限管理」，搜索并开通以下权限：

| 权限 | 说明 |
|------|------|
| `im:message` | 获取与发送消息 |
| `im:message:send_as_bot` | 以应用身份发消息 |

## 第四步：配置事件订阅

1. 进入「事件与回调」→「事件配置」
2. **请求地址**填写你的公网 URL：
   ```
   https://your-domain.com/feishu/webhook
   ```
3. 飞书会自动发送 challenge 验证请求，myclaw 会自动响应
4. 在「加密策略」中记录 **Verification Token**
5. 添加事件：搜索 `im.message.receive_v1`（接收消息 v2.0）

## 第五步：发布应用

1. 进入「版本管理与发布」
2. 创建新版本，填写版本号和更新说明
3. 提交审核（自建应用自己作为管理员可直接审批）

> 每次修改权限后都需要重新发布版本才能生效。

## 第六步：配置 myclaw

### 方式一：配置文件

编辑 `~/.myclaw/config.json`：

```json
{
  "channels": {
    "feishu": {
      "enabled": true,
      "appId": "cli_a5xxxxx",
      "appSecret": "your-app-secret",
      "verificationToken": "your-verification-token",
      "port": 9876,
      "allowFrom": []
    }
  }
}
```

### 方式二：环境变量

```bash
export MYCLAW_FEISHU_APP_ID="cli_a5xxxxx"
export MYCLAW_FEISHU_APP_SECRET="your-app-secret"
```

> 注意：`verificationToken` 和 `port` 只能通过配置文件设置。

### 方式三：交互式配置

```bash
make setup
```

## 配置参数说明

| 参数 | 类型 | 说明 |
|------|------|------|
| `enabled` | bool | 是否启用飞书通道 |
| `appId` | string | 飞书应用 App ID |
| `appSecret` | string | 飞书应用 App Secret |
| `verificationToken` | string | 事件订阅验证 Token（空 = 跳过验证） |
| `encryptKey` | string | 事件加密密钥（可选） |
| `port` | int | Webhook HTTP 服务端口（默认 9876） |
| `allowFrom` | []string | 允许的 open_id 列表（空 = 允许所有人） |

## 第七步：配置内网穿透

飞书事件订阅需要公网可访问的 URL。开发测试推荐使用 cloudflared：

### 临时隧道（开发测试）

```bash
# 安装 cloudflared
brew install cloudflared

# 启动隧道
make tunnel
# 或
cloudflared tunnel --url http://localhost:9876
```

输出中会显示一个临时 URL：
```
https://xxx-xxx-xxx.trycloudflare.com
```

将此 URL + `/feishu/webhook` 填入飞书事件订阅的请求地址。

> 注意：临时隧道每次重启 URL 会变，需要重新配置飞书事件订阅。

### 持久隧道（生产环境）

```bash
# 登录 Cloudflare
cloudflared tunnel login

# 创建隧道
cloudflared tunnel create myclaw

# 配置 DNS
cloudflared tunnel route dns myclaw feishu-bot.yourdomain.com

# 运行
cloudflared tunnel run myclaw
```

飞书事件订阅地址设为：`https://feishu-bot.yourdomain.com/feishu/webhook`

### Docker Compose 隧道

```bash
# 启动 myclaw + cloudflared 隧道
docker compose --profile tunnel up -d

# 查看隧道 URL
docker compose logs tunnel | grep trycloudflare
```

## 第八步：启动并测试

```bash
# 启动 gateway
make gateway
```

日志中看到以下内容表示成功：
```
[feishu] webhook server listening on :9876
[gateway] channels started: [feishu]
```

在飞书中搜索你的机器人名称，打开对话，发送消息测试。

## 常见问题

**Q: 飞书发消息没有反应？**
- 确认事件订阅 URL 已配置且 challenge 验证通过
- 确认已添加 `im.message.receive_v1` 事件
- 确认应用已发布且审核通过
- 检查 cloudflared 隧道是否正常运行

**Q: 收到 "Access denied" 错误？**
- 确认已开通 `im:message` 或 `im:message:send_as_bot` 权限
- 权限修改后需要重新发布应用版本

**Q: Webhook 返回 401？**
- 检查 `verificationToken` 是否与飞书开放平台一致
- 设为空字符串可跳过验证（仅开发测试用）

**Q: 如何获取用户的 open_id？**
- 查看 myclaw 日志中的 `inbound from feishu/ou_xxx` 信息
- `ou_xxx` 即为用户的 open_id，可添加到 `allowFrom`

**Q: 如何限制只有自己能用？**
- 先发一条消息，从日志获取你的 open_id
- 添加到 `allowFrom` 列表：
  ```json
  "allowFrom": ["ou_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"]
  ```
