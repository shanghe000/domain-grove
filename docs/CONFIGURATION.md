# 配置

运行示例使用本机开发身份和独立 SQLite 目录。环境变量由启动方提供。数据文件、访问凭据、信任证书和部署配置由运行环境管理。

| 环境变量 | 用途 |
| --- | --- |
| `GO_SERVER_ADDR` | 默认监听 `127.0.0.1:8080`。本机开发审核依赖回环监听。 |
| `DATA_DIR` | 图谱数据库与项目空间数据根目录。 |
| `PUBLIC_URL` | 工作台的 HTTP(S) 来源，用于浏览器写操作校验。外部身份验证要求 HTTPS。 |
| `INDEX_ALLOW_DEV_REVIEW` | 仅在回环开发环境设为 `true`，供本机演示审核。 |
| `MCP_API_TOKEN` / `MCP_SUBMIT_TOKEN` | 默认项目的读取与提交凭据。命名项目可在空间设置中分别签发。 |
| `LLM_API_KEY` | 模型服务密钥。按需设置。 |
| `LLM_BASE_URL` / `LLM_MODEL` | 模型端点和模型名。默认使用 DeepSeek 兼容接口。 |

## 外部身份验证

启用 `AUTH_ENABLED=true` 时，设置 HTTPS `AUTH_LOGIN_URL`、HTTPS `AUTH_VERIFY_TOKEN_URL` 和 `AUTH_APP_ID`。可通过 `AUTH_TLS_CA_FILE` 指定信任证书路径。由部署方提供登录和校验服务。

校验请求通过 Bearer 传递凭据，JSON 体内包含应用标识 `app_id`。校验服务使用 `active`、`user_id`、`username` 和可选的 `dept` 返回已授权身份。工作台依此继续检查项目角色。集成人员目录时使用应用的人员查询接口；当前默认以账号标识配置空间成员。

## 外部模型

开启模型后，建模请求携带指令、发布图谱及已提供的资料定位。只提交允许发送的数据。办完审核前，模型补丁停留在草案或待审队列。模型请求协议覆盖本地替身验证。

[索引契约与 MCP 工具](../INDEX-SPEC.md)
