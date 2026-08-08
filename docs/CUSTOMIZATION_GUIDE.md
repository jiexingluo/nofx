# NoFX 定制化指南

本文档基于对原始 NoFX 项目的深度定制和优化，涵盖策略配置详解和服务器部署流程。

---

## 一、策略优化

### 1.1 当前默认策略概览

策略名称：**AI500中长线资产配置策略-优化版(低回撤)**

核心设计思路：
- 风控优先，保护本金为第一目标
- 基于 AI500 热门币种榜单自动筛选候选币
- 多时间框架技术分析（15m / 1h / 4h）
- 全指标覆盖（EMA、MACD、RSI、ATR、BOLL、OI、资金费率）
- 严格的入场标准和禁止追高/追跌规则
- 基于 ATR 的动态止损止盈

### 1.2 参数详解

#### 1.2.1 币种来源 (`coin_source`)

| 参数 | 当前值 | 说明 |
|------|--------|------|
| `source_type` | `"ai500"` | 币种筛选来源，使用 AI500 评分榜单 |
| `ai500_limit` | `8` | 从 AI500 榜单取评分最高的前 N 个币种。值越大候选币越多，但 AI prompt 也越大，可能导致超时 |
| `use_oi_top` | `false` | 是否额外纳入 OI（持仓量）增长最快的币种 |
| `use_oi_low` | `false` | 是否额外纳入 OI 下降最快的币种 |

> **注意**：`ai500_limit` 是上限，实际候选币数量取决于 AI500 榜单当前有多少币上榜。

#### 1.2.2 K线与时间框架 (`indicators.klines`)

| 参数 | 当前值 | 说明 |
|------|--------|------|
| `primary_timeframe` | `"15m"` | 主时间框架，用于精确入场 |
| `primary_count` | `20` | 每个时间框架的 K 线数量。原始默认 30，为控制 prompt 大小降至 20 |
| `longer_timeframe` | `"4h"` | 较长时间框架，用于判断大趋势 |
| `longer_count` | `10` | 较长时间框架的 K 线数量 |
| `enable_multi_timeframe` | `true` | 启用多时间框架分析 |
| `selected_timeframes` | `["15m","1h","4h"]` | 实际使用的时间框架列表 |

#### 1.2.3 技术指标 (`indicators`)

| 参数 | 当前值 | 说明 |
|------|--------|------|
| `enable_ema` | `true` | 指数移动平均线，用于判断趋势方向 |
| `ema_periods` | `[20, 50]` | EMA 周期。EMA20 为短期趋势，EMA50 为中期趋势 |
| `enable_macd` | `true` | MACD 指标，用于确认动量方向 |
| `enable_rsi` | `true` | 相对强弱指标，用于判断超买超卖 |
| `rsi_periods` | `[7, 14]` | RSI 周期。RSI7 更灵敏，RSI14 更稳定 |
| `enable_atr` | `true` | 平均真实波幅，用于计算动态止损位 |
| `atr_periods` | `[14]` | ATR 周期 |
| `enable_boll` | `true` | 布林带，用于判断价格波动区间 |
| `boll_periods` | `[20]` | 布林带周期 |
| `enable_volume` | `true` | 成交量数据 |
| `enable_oi` | `true` | 持仓量（Open Interest）数据 |
| `enable_funding_rate` | `true` | 资金费率，反映多空力量对比 |
| `enable_raw_klines` | `true` | 原始 K 线数据（OHLCV） |

#### 1.2.4 量化数据与排行榜 (`indicators` 续)

| 参数 | 当前值 | 说明 |
|------|--------|------|
| `enable_quant_data` | `true` | 启用量化数据（OI 变化、资金流向等） |
| `enable_quant_oi` | `true` | 每个候选币的 OI 多时间框架变化数据 |
| `enable_quant_netflow` | `true` | 每个候选币的资金净流入/流出数据 |
| `enable_oi_ranking` | `true` | 全市场 OI 变化排行榜 |
| `oi_ranking_limit` | `10` | OI 排行榜显示条数 |
| `oi_ranking_duration` | `"1h"` | OI 排行榜统计时间窗口 |
| `enable_netflow_ranking` | `true` | 全市场资金流向排行榜 |
| `netflow_ranking_limit` | `10` | 资金流向排行榜显示条数 |
| `enable_price_ranking` | `true` | 全市场涨跌幅排行榜 |
| `price_ranking_limit` | `10` | 涨跌幅排行榜显示条数 |
| `price_ranking_duration` | `"1h,4h,24h"` | 涨跌幅排行榜统计时间窗口（多个用逗号分隔） |

> **Prompt 大小与 AI 超时**：以上排行榜和量化数据都会被拼入 AI prompt。如果候选币数量多（>5）且排行榜条数多（>5），prompt 可能超过 50KB，导致 AI 模型（如 qwen3.5-plus）在 180 秒内无法完成推理。建议根据实际情况调整 `ai500_limit`、`*_ranking_limit` 和 `primary_count`。

#### 1.2.5 风控参数 (`risk_control`)

| 参数 | 当前值 | 说明 |
|------|--------|------|
| `max_positions` | `2` | 最大同时持仓数量。超过此数量时禁止开新仓 |
| `btc_eth_max_leverage` | `10` | BTC/ETH 最大杠杆倍数 |
| `altcoin_max_leverage` | `5` | 山寨币最大杠杆倍数 |
| `btc_eth_max_position_value_ratio` | `3` | BTC/ETH 单笔仓位价值占账户权益的最大比例 |
| `altcoin_max_position_value_ratio` | `1.5` | 山寨币单笔仓位价值占账户权益的最大比例 |
| `max_margin_usage` | `0.7` | 最大保证金使用率（70%），超过后禁止开新仓 |
| `min_position_size` | `12` | 最小开仓金额（USDT） |
| `min_risk_reward_ratio` | `2.5` | 最低风险回报比，止盈距离必须 ≥ 2.5 倍止损距离 |
| `min_confidence` | `85` | AI 最低置信度阈值，低于此值不执行交易 |

#### 1.2.6 策略 Prompt (`prompt_sections`)

策略 prompt 由四个部分组成，定义了 AI 的交易行为：

| 部分 | 作用 |
|------|------|
| `role_definition` | 角色定义。设定 AI 为"风控优先"的量化交易员，核心原则包括单笔亏损不超过 5%、必须设置 ATR 止损、果断止损不死扛 |
| `trading_frequency` | 交易频率控制。限制每天 2-4 笔，每小时不超过 2 笔，单笔持仓 ≥ 30 分钟，止损后需冷静期 |
| `entry_standards` | 入场标准。要求多信号共振（EMA+RSI+MACD+ATR），禁止追高追跌（4h 涨跌超 30% 不入场），币种分散规则（同币最多连续交易 2 次） |
| `decision_process` | 决策流程。优先检查止损 → 检查止盈 → 扫描候选币 → 检查追高风险 → 计算仓位 → 输出决策 |

#### 1.2.7 决策权重 (`decision_weights`)

| 参数 | 当前值 | 说明 |
|------|--------|------|
| `technical_weight` | `50` | 技术分析权重 |
| `sentiment_weight` | `30` | 市场情绪权重（恐惧贪婪指数等） |
| `valuation_weight` | `10` | 估值分析权重 |
| `fundamental_weight` | `10` | 基本面分析权重 |

#### 1.2.8 情绪数据 (`sentiment_config`)

| 参数 | 当前值 | 说明 |
|------|--------|------|
| `enable_fear_greed_index` | `true` | 启用恐惧贪婪指数 |
| `enable_cryptoracle` | `false` | 是否启用 Cryptoracle 情绪数据 |

### 1.3 后续优化建议

#### 短期优化

1. **调整杠杆至更保守水平**：当前 BTC/ETH 最大杠杆 10x、山寨币 5x，回测验证 3x 杠杆时零爆仓。建议将 `btc_eth_max_leverage` 降至 3-5，`altcoin_max_leverage` 降至 3。

2. **提高技术分析权重**：当前 `technical_weight: 50`，回测中技术分析权重 70% 时表现更好。建议调整为 `technical_weight: 70, sentiment_weight: 15, valuation_weight: 10, fundamental_weight: 5`。

3. **优化 Prompt 大小**：如果 AI 响应时间经常超过 120 秒，可以：
   - 降低 `primary_count` 从 20 到 15
   - 降低 `*_ranking_limit` 从 10 到 5
   - 减少 `price_ranking_duration` 从 `"1h,4h,24h"` 到 `"1h,4h"`
   - 降低 `ai500_limit`（候选币数量）

4. **增加币种来源多样性**：开启 `use_oi_top: true` 可以纳入 OI 增长最快的币种，避免候选池过于单一。

#### 中期优化

5. **回测驱动的参数调优**：定期运行回测，对比不同参数组合的收益率、最大回撤、胜率、盈亏比，找到最优参数。

6. **根据市场状态动态调整**：
   - 牛市：可适当放宽 `min_confidence`（如 80）、提高 `max_positions`（如 3）
   - 熊市/震荡：收紧 `min_confidence`（如 90）、降低 `max_positions`（如 1）

7. **优化入场标准 Prompt**：根据实际交易记录分析 AI 的决策质量，针对性地调整 `entry_standards` 中的规则。例如：
   - 如果发现 AI 经常在震荡行情中亏损，加入"ADX < 25 时不入场"的规则
   - 如果发现止损过于频繁，调整 ATR 倍数从 2 到 2.5

#### 长期优化

8. **切换更强的 AI 模型**：当前使用 qwen3.5-plus，如果预算允许，可以切换到更强的模型（如 qwen-max 或 Claude）以获得更好的分析质量。

9. **增加经验学习**：利用系统的 experience 模块，让 AI 从历史交易中学习，避免重复犯错。

10. **多策略并行**：创建多个策略（如短线策略 + 中线策略），分配不同的资金比例，分散风险。

---

## 二、服务器部署指南

以下是将 NoFX 部署到远程 Linux 服务器（Ubuntu 22.04/24.04）的完整流程。

### 2.1 前置要求

- Ubuntu 22.04 或 24.04 服务器
- 至少 1 核 CPU、1GB 内存
- 已配置 SSH 访问
- 域名（可选，用于 HTTPS）

### 2.2 安装依赖

```bash
# 更新系统
sudo apt update && sudo apt upgrade -y

# 安装基础工具
sudo apt install -y git build-essential nginx

# 安装 Go（需要 1.24+）
wget https://go.dev/dl/go1.24.0.linux-amd64.tar.gz
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf go1.24.0.linux-amd64.tar.gz
echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.bashrc
source ~/.bashrc
go version  # 验证
```

### 2.3 构建项目

```bash
# 克隆代码（或从本地上传）
mkdir -p /opt/nofx && cd /opt/nofx
# 方式一：git clone
git clone <your-repo-url> .
# 方式二：从本地 scp
# scp -r /path/to/nofx/* user@server:/opt/nofx/

# 编译后端（必须启用 CGO，因为 SQLite 依赖）
CGO_ENABLED=1 go build -o nofx .

# 构建前端
cd web
npm install
npm run build
cd ..
```

> **重要**：不能使用 `CGO_ENABLED=0` 交叉编译，因为 `go-sqlite3` 需要 CGO。必须在目标服务器上直接编译。

### 2.4 环境变量配置

最新上游不再读取 `config.yaml`。从示例创建仅供本机使用的 `.env`，并生成长度不少于 32 字节的随机 JWT 密钥：

```bash
cd /opt/nofx
cp .env.example .env
JWT_SECRET_VALUE=$(openssl rand -base64 48)
sed -i "s|^JWT_SECRET=.*|JWT_SECRET=${JWT_SECRET_VALUE}|" .env
chmod 600 .env
```

确认或调整以下关键项：

```env
API_SERVER_PORT=8080
DB_TYPE=sqlite
DB_PATH=/opt/nofx/data/data.db
TRANSPORT_ENCRYPTION=false
EXPERIENCE_IMPROVEMENT=false

# 本地管理员认证绕过默认必须关闭。只有隔离的本地开发环境才可显式开启。
LOCAL_ADMIN_BYPASS_ENABLED=false
```

`JWT_SECRET` 不可为空、不可使用示例默认值，且至少为 32 字节，否则服务会拒绝启动。`LOCAL_ADMIN_BYPASS_ENABLED=true` 会允许无 token 或 `bypass-token` 以 `admin-default` 身份访问，并在启动时输出安全警告；不要在公网或生产环境启用。

创建数据目录：

```bash
mkdir -p /opt/nofx/data
```

### 2.5 Systemd 服务

创建 `/etc/systemd/system/nofx.service`：

```ini
[Unit]
Description=NoFX Trading Bot
After=network.target

[Service]
Type=simple
User=ubuntu
WorkingDirectory=/opt/nofx
ExecStart=/opt/nofx/nofx
Restart=always
RestartSec=5
Environment=GIN_MODE=release
EnvironmentFile=/opt/nofx/.env

[Install]
WantedBy=multi-user.target
```

启动服务：

```bash
sudo systemctl daemon-reload
sudo systemctl enable nofx
sudo systemctl start nofx

# 查看状态和日志
sudo systemctl status nofx
sudo journalctl -u nofx -f
```

### 2.6 Nginx 反向代理

创建 `/etc/nginx/sites-available/nofx`：

```nginx
server {
    listen 9527;
    server_name _;

    # 前端静态文件
    root /opt/nofx/web/dist;
    index index.html;

    # API 反向代理
    location /api/ {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_read_timeout 300s;
    }

    # SSE（Server-Sent Events）支持
    location /api/events {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_http_version 1.1;
        proxy_set_header Connection "";
        proxy_buffering off;
        proxy_cache off;
        proxy_read_timeout 86400s;
    }

    # WebSocket 支持
    location /ws {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
    }

    # SPA 路由回退
    location / {
        try_files $uri $uri/ /index.html;
    }
}
```

启用配置：

```bash
sudo ln -sf /etc/nginx/sites-available/nofx /etc/nginx/sites-enabled/
sudo rm -f /etc/nginx/sites-enabled/default
sudo nginx -t
sudo systemctl reload nginx
```

### 2.7 防火墙配置

```bash
sudo ufw allow 22/tcp      # SSH
sudo ufw allow 9527/tcp    # NoFX Web
sudo ufw --force enable
sudo ufw status
```

### 2.8 安全加固（推荐）

```bash
# SSH 安全
sudo sed -i 's/#MaxAuthTries.*/MaxAuthTries 3/' /etc/ssh/sshd_config
sudo sed -i 's/PermitRootLogin yes/PermitRootLogin no/' /etc/ssh/sshd_config
sudo systemctl restart sshd

# 安装 fail2ban 防暴力破解
sudo apt install -y fail2ban
sudo systemctl enable fail2ban
sudo systemctl start fail2ban

# Nginx 安全头
# 在 nginx server 块中添加：
#   add_header X-Frame-Options SAMEORIGIN;
#   add_header X-Content-Type-Options nosniff;
#   add_header X-XSS-Protection "1; mode=block";
#   add_header Referrer-Policy strict-origin-when-cross-origin;

# 文件权限
sudo chmod 600 /opt/nofx/.env
sudo chmod 700 /opt/nofx/data
```

### 2.9 导入策略并启动交易

部署完成后，通过 Web 界面（`http://<服务器IP>:9527`）操作：

1. 进入「策略工作室」创建或导入策略
2. 配置 AI 模型（如阿里云 DashScope 的 qwen3.5-plus）
3. 配置交易所 API Key（Binance 等）
4. 创建交易机器人，选择策略并启动

也可以通过 API 导入策略：

```bash
# 导出本地策略
curl -s http://localhost:8080/api/strategies/<策略ID> -o strategy.json

# 导入到服务器
curl -X POST http://<服务器IP>:9527/api/strategies \
  -H "Content-Type: application/json" \
  -d @strategy.json
```

### 2.10 日常运维

```bash
# 查看实时日志
sudo journalctl -u nofx -f

# 查看 AI 调用 payload 大小（排查超时问题）
sudo journalctl -u nofx | grep "payload size"

# 重启服务
sudo systemctl restart nofx

# 更新代码后重新部署
cd /opt/nofx
git pull
CGO_ENABLED=1 go build -o nofx .
cd web && npm run build && cd ..
sudo systemctl restart nofx
```

### 2.11 常见问题

| 问题 | 原因 | 解决方案 |
|------|------|---------|
| AI 调用超时 (context deadline exceeded) | Prompt 过大，AI 模型无法在 180s 内处理 | 减少 `ai500_limit`、`primary_count`、`*_ranking_limit` |
| `go-sqlite3 requires cgo to work` | 使用了 `CGO_ENABLED=0` 编译 | 必须 `CGO_ENABLED=1` 在目标机器上编译 |
| `JWT_SECRET is required` 或长度不足 | `.env` 中缺少安全的 JWT 密钥 | 执行 `openssl rand -base64 48` 并写入 `JWT_SECRET` |
| SSH 连接被拒绝 | fail2ban 封禁了 IP | 通过云控制台 VNC 登录，执行 `sudo fail2ban-client set sshd unbanip <IP>` |
| 前端页面空白 | Nginx 未正确配置 SPA 回退 | 确保 `try_files $uri $uri/ /index.html` 配置正确 |
