from __future__ import annotations

import json
import re
import threading
import time
from urllib.parse import quote, unquote

from settings import Settings, WorkerError, endpoint, fetch_json, public_url, split_url

DOCUMENT_PATH = re.compile(r"/(docx|wiki)/([A-Za-z0-9_-]{3,128})/?\Z")
DOC_ID = re.compile(r"[A-Za-z0-9_-]{3,128}\Z")
TICKS = chr(96) * 3
TEXT_BLOCKS = (*[f"heading{n}" for n in range(1, 10)], "text", "bullet", "ordered", "code", "quote", "todo")


def feishu_link(settings: Settings, link: str):
    try:
        url = split_url(link)
    except WorkerError:
        return None
    host = url.hostname
    if (url.scheme != "https" or url.port not in (None, 443)
            or not any(host == base or host.endswith("." + base) for base in settings.feishu_hosts)):
        return None
    return DOCUMENT_PATH.fullmatch(url.path)


def inline(elements: list, *, raw: bool = False) -> str:
    spans = []
    for element in elements:
        if not isinstance(element, dict):
            continue
        run = element.get("text_run")
        if not isinstance(run, dict):
            mention = element.get("mention_doc") or element.get("equation") or {}
            spans.append(str(mention.get("title") or mention.get("content") or ""))
            continue
        text = str(run.get("content", ""))
        style = run.get("text_element_style") or {}
        if text and not raw:
            for field, marker in (("inline_code", chr(96)), ("bold", "**"), ("italic", "*"), ("strikethrough", "~~")):
                if style.get(field):
                    text = marker + text + marker
            if style.get("background_color"):
                text = "==" + text + "=="
            link = (style.get("link") or {}).get("url")
            if isinstance(link, str):
                destination = unquote(link)
                try:
                    split_url(destination)
                    text = f"[{text}]({quote(destination, safe=':/?&=%+#@,;~!-._')})"
                except WorkerError:
                    pass
        spans.append(text)
    return "".join(spans)


def blocks_markdown(blocks: list[dict]) -> str:
    indexed = {block["block_id"]: block for block in blocks if block.get("block_id")}
    visited: set[str] = set()

    def render(block_id: str, depth: int = 0) -> str:
        if block_id in visited or block_id not in indexed or depth > 64:
            return ""
        visited.add(block_id)
        block = indexed[block_id]
        children = block.get("children") or []
        table = block.get("table")
        if isinstance(table, dict):
            properties = table.get("property") or {}
            columns = properties.get("column_size", 0)
            if isinstance(columns, int) and 0 < columns <= 128:
                cells = [render(cell, depth + 1).strip().replace("|", "\\|").replace("\n", "<br>")
                         for cell in (table.get("cells") or children)]
                rows = [cells[index:index + columns] for index in range(0, len(cells), columns)]
                if rows:
                    if properties.get("header_row") is False:
                        rows.insert(0, [""] * columns)
                    rows.insert(1, ["---"] * columns)
                    return "\n".join("| " + " | ".join(row + [""] * (columns - len(row))) + " |" for row in rows)
        if "callout" in block or "quote_container" in block:
            body = "\n\n".join(filter(None, (render(child, depth + 1) for child in children)))
            prefix = "> [!NOTE]\n" if "callout" in block else ""
            return prefix + "\n".join("> " + line if line else ">" for line in body.splitlines())
        kind = next((key for key in TEXT_BLOCKS if isinstance(block.get(key), dict)), "")
        value = block.get(kind, {})
        text = inline(value.get("elements") or [], raw=kind == "code")
        list_indent = "  "
        if kind != "code":
            text = text.strip()
        if kind.startswith("heading"):
            text = "#" * min(int(kind[7:]), 6) + " " + text
        elif kind == "quote":
            text = "\n".join("> " + line if line else ">" for line in text.split("\n"))
        elif kind in {"bullet", "ordered", "todo"}:
            sequence = (value.get("style") or {}).get("sequence")
            ordered_number = sequence if isinstance(sequence, str) and re.fullmatch(r"[0-9]{1,8}", sequence) else "1"
            prefix = {"bullet": "- ", "ordered": ordered_number + ". ",
                      "todo": "- [x] " if (value.get("style") or {}).get("done") else "- [ ] "}[kind]
            if kind == "ordered":
                list_indent = " " * len(prefix)
            text = prefix + text.replace("\n", "\n" + list_indent)
        elif kind == "code":
            fence = TICKS
            while fence in text:
                fence += chr(96)
            text = f"{fence}\n{text}\n{fence}"
        elif "divider" in block:
            text = "---"
        descendants = list(filter(None, (render(child, depth + 1) for child in children)))
        if descendants and kind in {"bullet", "ordered", "todo"}:
            return text + "\n" + "\n".join(list_indent + line for line in "\n".join(descendants).splitlines())
        return "\n\n".join(filter(None, [text, *descendants]))

    roots = [block_id for block_id, block in indexed.items() if block.get("parent_id") not in indexed]
    chunks = [render(block_id) for block_id in roots]
    chunks.extend(render(block_id) for block_id in indexed if block_id not in visited)
    return "\n\n".join(filter(None, chunks)).strip()


class Feishu:
    def __init__(self, settings: Settings):
        self.settings = settings
        self.lock = threading.Lock()
        self.token: str = ""
        self.expires: float = 0

    @staticmethod
    def checked(result: dict) -> dict:
        if result.get("code") != 0:
            code = result.get("code")
            suffix = f"（错误码 {code}）" if isinstance(code, int) else ""
            raise WorkerError("飞书未开放所需读取权限或资料不存在" + suffix + "。", 403)
        return result

    def tenant_token(self, base: str) -> str:
        with self.lock:
            if self.token and time.monotonic() < self.expires:
                return self.token
            if not self.settings.feishu_app or not self.settings.feishu_secret:
                raise WorkerError("尚未配置 FEISHU_APP_ID 或 FEISHU_APP_SECRET。", 503)
            result = self.checked(fetch_json(base + "/auth/v3/tenant_access_token/internal", method="POST",
                                             service="飞书令牌服务", json={"app_id": self.settings.feishu_app,
                                                                         "app_secret": self.settings.feishu_secret}))
            token = result.get("tenant_access_token")
            if not isinstance(token, str) or not token:
                raise WorkerError("飞书未返回机器人访问令牌。", 502)
            self.token = token
            lifetime = result.get("expire", 600)
            self.expires = time.monotonic() + max(0, (lifetime if isinstance(lifetime, int) else 600) - 90)
            return self.token

    def read(self, link: str) -> dict:
        match = feishu_link(self.settings, link)
        if not match:
            raise WorkerError("请粘贴飞书新版文档或指向新版文档的知识库链接。")
        link = public_url(link)
        kind, doc_id = match.groups()
        if self.settings.mock:
            return {"doc_id": doc_id, "url": link, "title": "阅读挑战 · 留存分析示例",
                    "markdown": """## 业务定义

阅读挑战向已完成新手引导的读者开放。参与人数按账号去重。

> [!NOTE]
> 留存复盘应同时核对投放时间和实验分流。

| 条件 | 徽章代码 |
| --- | --- |
| 记录第一次阅读 | FIRST |
| 阅读三种题材 | VARIETY |

### 埋点口径

- 按账号首次注册入组。
- 阅读会话用于区分回访与持续阅读。
"""}
        base = endpoint(self.settings.feishu_base, "FEISHU_API_BASE_URL").geturl().rstrip("/")
        headers = {"Authorization": f"Bearer {self.tenant_token(base)}"}

        def call(path, **params):
            result = self.checked(fetch_json(base + path, service="飞书云文档", headers=headers, params=params))
            data = result.get("data", {})
            return data if isinstance(data, dict) else {}

        if kind == "wiki":
            node = call("/wiki/v2/spaces/get_node", token=doc_id).get("node", {})
            doc_id = node.get("obj_token", "")
            if node.get("obj_type") != "docx" or not isinstance(doc_id, str) or not DOC_ID.fullmatch(doc_id):
                raise WorkerError("知识库节点没有指向新版飞书文档。")
        doc = call(f"/docx/v1/documents/{doc_id}").get("document", {})
        title = str(doc.get("title") or "飞书文档")[:160]
        revision = doc.get("revision_id")
        blocks, page_token, pages, total_bytes = [], "", set(), 0
        for _ in range(32):
            params = {"page_size": 200, "document_revision_id": revision if isinstance(revision, int) else -1}
            if page_token:
                params["page_token"] = page_token
            page = call(f"/docx/v1/documents/{doc_id}/blocks", **params)
            items = page.get("items")
            if not isinstance(items, list):
                raise WorkerError("飞书未返回文档区块。", 502)
            blocks.extend(item for item in items if isinstance(item, dict))
            total_bytes += len(json.dumps(items, ensure_ascii=False).encode("utf-8"))
            if total_bytes > 5_000_000:
                raise WorkerError("文档区块超过处理上限，请拆分后再导入。", 413)
            if not page.get("has_more"):
                markdown = blocks_markdown(blocks)
                if not markdown:
                    raise WorkerError("该文档没有可清洗的文字内容。")
                return {"doc_id": doc_id, "url": link, "title": title, "markdown": markdown}
            page_token = page.get("page_token")
            if not isinstance(page_token, str) or not page_token or page_token in pages:
                break
            pages.add(page_token)
        raise WorkerError("飞书文档分页未完整结束，请重试。", 502)
