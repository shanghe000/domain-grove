from index_anchors import SourceLines, refresh_snapshot


def test_markdown_section_drift_and_ambiguity():
    src = SourceLines("新增段落\n\n## 统计\n资料\n### 维度\n字段\n## 同名\n## 同名\n", ".md")
    n = {"kind":"artifact","id":"doc","ref":{"repository":"bi","path":"doc.md","anchor":"统计","line_start":1}}
    old = {"id":"e","from":"a","to":"doc","relation":"defined_in","anchor":"同名","line_start":8}
    change = {"id":"t","ref":n["ref"]}
    patch, unresolved = refresh_snapshot({"edges":[old],"changes":[change]}, n, src, "updated")
    assert patch["upsert_nodes"][0]["ref"]["line_start"] == 3
    assert patch["upsert_nodes"][0]["ref"]["line_end"] == 6
    assert patch["upsert_changes"][0]["ref"]["line_start"] == 3
    assert unresolved == ["同名"] and "line_start" not in patch["upsert_edges"][0]
    assert old["line_start"] == 8


def test_csv_physical_lines_and_contiguous_event_rows():
    src = SourceLines('name,field\nlogin,"a\nb"\nlogin,id\nother,id\n', ".csv")
    assert src.unique_span("login") == (2, 4)
    assert src.unique_span("field") == (1, 1)
    assert SourceLines("name\nid\na\nid",".csv").unique_span("id") is None


def test_ignore_code_fences_and_anchor_at_quote():
    src = SourceLines("# 标题\n~~~\n## 伪标题\n~~~\n内容\n", ".md")
    assert src.heading_at(5) == "标题"
    assert src.unique_span("伪标题") is None
