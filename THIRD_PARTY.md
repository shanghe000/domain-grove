# 第三方依赖

本项目源码采用 [MIT 许可](LICENSE)。Go、Node.js 与 Python 依赖分别遵循其自身许可证。源码仓库通过依赖清单引用它们。第三方源码和打包产物的分发应保留相应版本的许可和署名文件。

| 依赖 | 上游许可信息 |
| --- | --- |
| MCP Go SDK | [许可证与授权过渡说明](https://github.com/modelcontextprotocol/go-sdk/blob/v1.8.0/LICENSE) |
| YAML v3 | [上游项目及许可证](https://github.com/yaml/go-yaml/tree/v3.0.4) |
| SQLite Go 驱动 | [上游仓库](https://gitlab.com/cznic/sqlite) |
| React | [MIT License](https://github.com/facebook/react/blob/v18.3.1/LICENSE) |
| D3 Force | [ISC License](https://github.com/d3/d3-force/blob/v3.0.0/LICENSE) |

Go 的版本和摘要见 [go.mod](go.mod)、[go.sum](go.sum)。前端依赖见 [package-lock.json](web/package-lock.json)，可选 Python 适配器依赖见 [index-requirements.txt](workers/extractor/index-requirements.txt)。
