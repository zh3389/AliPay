# 支付宝订单码支付 SCF Serverless 网关后端

基于 **腾讯云 SCF Go 函数**（`tencent-serverless-go` 框架）的极简支付宝支付后端，实现方式对齐微信支付参考项目 `PayWeChat`。

## 特性

- **订单码支付**（对应微信 Native，返回 `qr_code`，用户扫码支付）
- 无官方 Go SDK，用标准库实现支付宝官方 **RSA2 签名协议**（SHA256withRSA），安全级别等同官方 SDK
- 金额单位为**分**（内部自动转支付宝的"元"，与微信支付接口对齐，前端零改动）
- SCF 静态函数 + 函数 URL，全部密钥从环境变量/文件读取，零硬编码
- 4 个核心接口：下单、退款、回调验签、查询订单

## API 接口

| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/pay/precreate` | 订单码下单（返回 `qr_code`） |
| POST | `/pay/refund` | 申请退款 |
| POST | `/pay/notify` | 支付宝异步通知（自动 RSA2 验签） |
| GET  | `/pay/query?out_trade_no=xxx` | 查询订单 |
| GET  | `/pay/health` | 健康检查（含初始化错误信息） |

### 请求示例

**订单码下单**
```bash
curl -X POST https://<function-url>/pay/precreate \
  -H "Content-Type: application/json" \
  -d '{"description":"测试商品","amount":1}'
# amount 单位：分（1 = 0.01 元）
# 返回: {"code":0,"data":{"out_trade_no":"SCF...","qr_code":"https://qr.alipay.com/..."}}
```

**退款**
```bash
curl -X POST https://<function-url>/pay/refund \
  -H "Content-Type: application/json" \
  -d '{"out_trade_no":"SCF20260101120000","refund":1,"reason":"商品退货"}'
```

**查询订单**
```bash
curl "https://<function-url>/pay/query?out_trade_no=SCF20260101120000"
# 关注 trade_status: WAIT_BUYER_PAY / TRADE_SUCCESS / TRADE_FINISHED / TRADE_CLOSED
```

## 环境变量

在 SCF 控制台「函数管理 → 函数配置 → 环境变量」中设置：

| 变量名 | 说明 | 示例 |
|--------|------|------|
| `ALIPAY_APP_ID` | 应用 AppID | `xxxxxxxxxxxxxxxx` |
| `ALIPAY_GATEWAY` | 网关地址（默认沙箱网关，正式环境必须覆盖） | `https://openapi-sandbox.dl.alipaydev.com/gateway.do` |
| `ALIPAY_NOTIFY_URL` | 异步通知回调地址 | `https://<function-url>/pay/notify` |
| `ALIPAY_SELLER_ID` | 商家 PID（可选，沙箱为 `xxxxxxxxxxxxxxxx`） | `xxxxxxxxxxxxxxxx` |

### 密钥加载（二选一）

**方式一：环境变量（推荐，生产环境）**

| 变量名 | 内容 |
|--------|------|
| `ALIPAY_PRIVATE_KEY` | 应用私钥完整 PEM 文本 |
| `ALIPAY_PUBLIC_KEY` | 支付宝公钥完整 PEM 文本 |

**方式二：文件路径（本地测试 / 打包到 zip）**

| 变量名 | 内容 |
|--------|------|
| `ALIPAY_PRIVATE_KEY_PATH` | 私钥文件路径（默认 `keys/app_private_key.pem`） |
| `ALIPAY_PUBLIC_KEY_PATH` | 公钥文件路径（默认 `keys/alipay_public_key.pem`） |

> 本仓库 `keys/` 目录已放置沙箱密钥（已被 `.gitignore` 忽略，勿提交公网）。

## 编译打包

```bash
# 1. 整理依赖（国内网络建议 GOPROXY=https://goproxy.cn,direct）
make tidy

# 2. 编译打包（仅二进制）→ 产出 main.zip
make build

# 3. 打包含密钥（可选，本地测试用）
make build-with-keys
```

## SCF 控制台部署（图文步骤）

### 第一步：新建函数

打开 [SCF 控制台](https://console.cloud.tencent.com/scf/list) → 选择地域（如**成都**）→「新建」→ 选择 **「从头开始」**。

### 第二步：基础配置

| 字段 | 填写值 |
|------|--------|
| 函数类型 | `事件函数` |
| 函数名称 | `alipay` |
| 运行环境 | `Go 1`（⚠️ 必须选 Go 1） |
| 提交方法 | `本地上传zip包`（Go 运行时不支持在线编辑） |
| 执行方法 | `main` |

上传 `make build` 产出的 `main.zip`。

### 第三步：高级配置（关键）

- **内存**：`256MB`（RSA 签名需要，128MB 偏小）
- **执行超时时间**：`60 秒`（默认 3 秒太短！）
- **环境变量**：按上表逐行添加（PEM 换行被转义为字面 `\n` 时，代码已自动处理，直接粘贴即可）

### 第四步：触发器（函数 URL）

> ⚠️ API 网关触发器已于 2025-06-30 停止服务，请使用**函数 URL**。

1. 创建触发器 → 触发方式选择 **「函数 URL」** → 鉴权类型 `免鉴权`
2. 获得 URL：`https://<function-url>.ap-chengdu.tencentscfapp.com/`
3. 将 `ALIPAY_NOTIFY_URL` 回填为 `https://<function-url>.ap-chengdu.tencentscfapp.com/pay/notify`

### 验证

```bash
# 健康检查
curl https://<function-url>.ap-chengdu.tencentscf.com/pay/health

# 下单测试（1 分钱）
./test.sh precreate
# 用沙箱买家账号 xxxxxxxxxx@sandbox.com 登录【沙箱支付宝 App】扫码支付
# 支付密码 xxxxxx
```

## 验签失败排查（重要）

若请求能下单成功（`code=10000`），但响应报「响应验签失败」，说明 **`支付宝公钥` 与当前沙箱环境不匹配**（应用私钥已由下单成功证明正确）。

请在沙箱控制台重新复制当前生效的支付宝公钥：

1. 打开 [沙箱应用管理](https://openhome.alipay.com/develop/sandbox/app) → 你的沙箱应用
2. 「应用信息 → 开发设置 → 接口加签方式」→ 复制「支付宝公钥」
3. 替换 `keys/alipay_public_key.pem` 中内容（或更新环境变量 `ALIPAY_PUBLIC_KEY`）

> 若此前更换过密钥或切换过加签模式（公钥/证书），平台公钥会变化，必须同步更新。
> 更新后运行 `go test -run TestPrecreateSandbox -v` 验证全链路。

## 沙箱测试账号

| 角色 | 账号 | 密码 |
|------|------|------|
| 商家 | `xxxxxxxxxx@sandbox.com` | `xxxxxx` |
| 买家 | `xxxxxxxxxx@sandbox.com` | `xxxxxx`（支付密码同） |

## 安全说明

- 私钥绝不硬编码，全部从环境变量/文件读取，`keys/` 已加入 `.gitignore`
- 请求加签：应用私钥 RSA2；响应/异步通知验签：支付宝公钥 RSA2，拒绝伪造请求
- 异步通知务必校验 `trade_status`、`total_amount`、`app_id` 一致性后再更新订单
- 正式上线：将 `ALIPAY_GATEWAY` 改为 `https://openapi.alipay.com/gateway.do`，替换为正式应用 AppID/密钥/支付宝公钥
