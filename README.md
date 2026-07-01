# LLM Tester

轻量级 LLM API 测试桌面工具。支持多种 API 格式的统一测试、基准对比和压力测试。

## 快速开始

```bash
# 下载对应平台的二进制后
./llm-tester

# 或自行编译
go build ./cmd/llm-tester/ && ./llm-tester

# 打开浏览器访问
open http://localhost:8912
```

## 功能

| 功能 | 说明 |
|------|------|
| **配置管理** | 多配置 CRUD，预设模板，JSON 导入/导出，名称搜索过滤 |
| **连接测试** | 验证 API 连通性并获取可用模型列表 |
| **聊天测试** | 单条消息测试，展示延迟和 Token 统计 |
| **批量测试** | 多配置同时测试，SSE 流式推送结果 |
| **基准测试** | 并发多轮测试，P50/P95/标准差延迟统计，结果导出 JSON/CSV |
| **压力测试** | 长时连续测试，追踪总 Token 消耗 |
| **代理支持** | 配置级别代理 + 环境变量兜底 |

## 支持的 Provider

- OpenAI 兼容（OpenAI / DeepSeek / Kimi / GLM-4 / 通义千问）
- Ollama（本地模型）
- Azure OpenAI Service
- 自定义端点

## 安装

### 从 Release 下载

访问 [Releases](https://github.com/qwdingyu/llm-tester/releases) 页面下载对应平台的二进制文件。

### 从源码编译

```bash
git clone https://github.com/qwdingyu/llm-tester.git
cd llm-tester
go build ./cmd/llm-tester/
```

## 使用

1. 启动后访问 `http://localhost:8912`
2. 在「配置管理」标签页添加 API 配置
3. 切换到「连接测试」验证连通性
4. 使用「聊天测试」「基准测试」「压力测试」等功能

端口可通过环境变量修改：`PORT=9090 ./llm-tester`

## 构建

```bash
# 当前平台
./build.sh          # macOS / Linux
.\build.ps1         # Windows

# 全平台交叉编译
make build-all      # → dist/ 目录

# 打包发布
make release        # → dist/*.tar.gz / *.zip
```

## 技术栈

- **后端**: Go 1.23 + Gin
- **前端**: Vue 3 (CDM ESM, 无构建工具)
- **部署**: 单二进制（Go embed）

## 文档

- [使用说明](docs/03_使用说明_20260701.md)
- [构建与发布流程](docs/02_发布与构建流程.md)
- [开发规则](docs/01_开发规则.md)
- [采坑记录](docs/04_采坑记录_20260701.md)

## License

MIT
