import pytest

import feishu_to_md
from feishu_to_md import Feishu, blocks_markdown, feishu_link
from settings import Settings, WorkerError


def text_block(block_id, parent, kind, content, children=None):
    return {
        "block_id": block_id,
        "parent_id": parent,
        "children": children or [],
        kind: {"elements": [{"text_run": {"content": content}}]},
    }


def test_wiki_resolution_pagination_and_markdown(monkeypatch):
    settings = Settings(mock=False, feishu_app="fixture-app", feishu_secret="fixture-secret")
    children = ["title", "notice", "listing", "script", "table"]
    first_page = [
        {"block_id": "DocXYZ", "children": children, "page": {}},
        text_block("title", "DocXYZ", "heading2", "留存口径"),
        {"block_id": "notice", "parent_id": "DocXYZ", "children": ["warning"], "callout": {}},
        text_block("warning", "notice", "text", "按账号统计"),
        text_block("listing", "DocXYZ", "bullet", "活动", ["child"]),
    ]
    second_page = [
        text_block("child", "listing", "ordered", "公会战"),
        text_block("script", "DocXYZ", "code", "    select 1"),
        {"block_id": "table", "parent_id": "DocXYZ", "table": {
            "property": {"column_size": 2}, "cells": ["cell1", "cell2", "cell3", "cell4"],
        }},
        *(text_block(f"cell{i}", "table", "text", value)
          for i, value in enumerate(["埋点", "业务字段", "first_login", "账号"], 1)),
    ]
    requests = []

    def fake_fetch(url, **kwargs):
        path = url.removeprefix(settings.feishu_base)
        requests.append(path)
        if path == "/auth/v3/tenant_access_token/internal":
            assert kwargs["json"] == {"app_id": "fixture-app", "app_secret": "fixture-secret"}
            assert kwargs["method"] == "POST"
            return {"code": 0, "tenant_access_token": "fixture-tenant", "expire": 3600}
        assert kwargs["headers"] == {"Authorization": "Bearer fixture-tenant"}
        if path == "/wiki/v2/spaces/get_node":
            assert kwargs["params"] == {"token": "AbcWiki"}
            result = {"node": {"obj_type": "docx", "obj_token": "DocXYZ"}}
        elif path == "/docx/v1/documents/DocXYZ":
            result = {"document": {"title": "留存策划案", "revision_id": 42}}
        elif path == "/docx/v1/documents/DocXYZ/blocks":
            assert kwargs["params"]["document_revision_id"] == 42
            if not kwargs["params"].get("page_token"):
                result = {"items": first_page, "has_more": True, "page_token": "page-2"}
            else:
                assert kwargs["params"]["page_token"] == "page-2"
                result = {"items": second_page, "has_more": False}
        else:
            raise AssertionError(path)
        return {"code": 0, "data": result}

    monkeypatch.setattr(feishu_to_md, "fetch_json", fake_fetch)
    client = Feishu(settings)
    source = client.read("https://team.feishu.cn/wiki/AbcWiki?ignored=secret#anchor")
    assert source["doc_id"] == "DocXYZ"
    assert source["url"] == "https://team.feishu.cn/wiki/AbcWiki"
    assert source["title"] == "留存策划案"
    markdown = source["markdown"]
    assert "## 留存口径" in markdown
    assert "> [!NOTE]\n> 按账号统计" in markdown
    assert "- 活动\n  1. 公会战" in markdown
    assert "```\n    select 1\n```" in markdown
    assert "| 埋点 | 业务字段 |\n| --- | --- |\n| first_login | 账号 |" in markdown
    assert markdown.count("first_login") == 1
    client.read("https://team.feishu.cn/docx/DocXYZ")
    assert requests.count("/auth/v3/tenant_access_token/internal") == 1


def test_markdown_preserves_numbered_nesting_quotes_and_headerless_tables():
    ordered = text_block("ordered", "", "ordered", "第十条\n说明", ["nested"])
    ordered["ordered"]["style"] = {"sequence": "10"}
    markdown = blocks_markdown([
        ordered,
        text_block("nested", "ordered", "bullet", "规则细节"),
        text_block("quote", "", "quote", "引用第一行\n引用第二行"),
        {"block_id": "table", "table": {"property": {"column_size": 2, "header_row": False},
                                        "cells": ["cell1", "cell2"]}},
        text_block("cell1", "table", "text", "第一条数据"),
        text_block("cell2", "table", "text", "423"),
    ])
    assert "10. 第十条\n    说明\n    - 规则细节" in markdown
    assert "> 引用第一行\n> 引用第二行" in markdown
    assert "|  |  |\n| --- | --- |\n| 第一条数据 | 423 |" in markdown


@pytest.mark.parametrize("url", [
    "https://team.feishu.cn.evil.invalid/docx/document",
    "https://team.feishu.cn@elsewhere.invalid/docx/document",
    "https://team.feishu.cn:8000/docx/document",
    "http://team.feishu.cn/docx/document",
    "https://team.feishu.cn/sheets/document",
])
def test_rejects_non_document_sources(url):
    settings = Settings(mock=True)
    assert feishu_link(settings, url) is None
    with pytest.raises(WorkerError):
        Feishu(settings).read(url)
