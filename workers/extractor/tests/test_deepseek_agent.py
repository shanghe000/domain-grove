import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from threading import Thread
from types import SimpleNamespace

from fastapi.testclient import TestClient

from deepseek_agent import CONFIG, FEISHU, TRACKING, DeepSeek, source_plan
from main import create_app
from settings import Settings
from test_worker import xlsx


def test_openai_protocol_routes_and_extracts_freeform_header(tmp_path):
    calls = []

    class Provider(BaseHTTPRequestHandler):
        def do_POST(self):
            body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
            calls.append((self.path, self.headers.get("Authorization"), body))
            if body.get("tools"):
                message = {"role": "assistant", "content": None, "tool_calls": [{
                    "id": "tool_workbook", "type": "function",
                    "function": {"name": CONFIG, "arguments": '{"url_or_file":"ReadingChallenge.xlsx"}'},
                }]}
            else:
                message = {"role": "assistant", "content": json.dumps({
                    "table_name": "cfg_readingchallenge", "header_data_start_row": 4, "primary_key": "challenge_id",
                    "columns": [
                        {"name": "challenge_id", "duckdb_type": "BIGINT", "comment": "挑战主键", "confidence": "high"},
                        {"name": "hours", "duckdb_type": "DOUBLE", "comment": "阅读时长估算", "confidence": "low"},
                    ],
                })}
            response = json.dumps({"id":"local_test", "model":"test-model", "created":1, "object":"chat.completion",
                                   "choices":[{"index":0, "finish_reason":"tool_calls" if body.get("tools") else "stop",
                                               "message":message}]}).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(response)))
            self.end_headers()
            self.wfile.write(response)

        def log_message(self, *_args):
            pass

    provider = ThreadingHTTPServer(("127.0.0.1", 0), Provider)
    thread = Thread(target=provider.serve_forever, daemon=True)
    thread.start()
    settings = Settings(data_dir=tmp_path, duckdb_path=tmp_path / "config.duckdb", mock=False,
                        llm_base=f"http://127.0.0.1:{provider.server_port}/v1", llm_key="test-token",
                        llm_model="test-model")
    workbook = xlsx("ReadingChallenge.xlsx", [["阅读挑战示例", None], ["挑战主键", "阅读时长估算"],
                                               ["BIGINT", "DOUBLE"], [401, 2.4]])
    try:
        with TestClient(create_app(settings)) as client:
            response = client.post("/chat", json={"session_id":"model", "message":"导入上传的配置表",
                                                   "uploaded_files":[workbook]})
    finally:
        provider.shutdown()
        provider.server_close()
        thread.join()

    assert response.status_code == 200, response.text
    schema = response.json()["draft_patch"]["artifacts"][0]["schema_meta"]
    assert schema["header_data_start_row"] == 4
    assert schema["primary_key"] == "challenge_id"
    assert schema["columns"][1]["confidence"] == "low"
    assert len(calls) == 2
    for path, authorization, body in calls:
        assert path == "/v1/chat/completions"
        assert authorization == "Bearer test-token"
        assert body["thinking"] == {"type":"disabled"}
        assert body["model"] == "test-model"
        assert "max_tokens" in body
    assert calls[0][2]["tool_choice"] == "auto"
    assert calls[1][2]["response_format"] == {"type":"json_object"}


def test_live_routes_only_valid_model_tools_and_redacts_url_credentials():
    config_url = "https://config.example.com/tracking/v20990101/ReadingChallenge.xlsx?token=fixture-token"
    document_url = "https://team.feishu.cn/docx/ABC123?id=private-id"
    message = f"仅列出这几个链接的格式 {config_url} {document_url} https://fixture-user:fixture-password@example.com"

    class Router:
        def route(self, text, sources, *_):
            assert "fixture-token" not in text and "fixture-password" not in text and "private-id" not in text
            assert sources == ["https://config.example.com/tracking/v20990101/ReadingChallenge.xlsx", "https://team.feishu.cn/docx/ABC123"]
            return [(TRACKING, "v20990101"), (FEISHU, "https://team.feishu.cn/docx/Unknown")], "仅说明链接格式。"

    result = source_plan(Settings(mock=False), Router(), message, {}, {}, {})
    assert result.sources == []
    assert result.answer == "仅说明链接格式。"


def test_live_tracking_preserves_full_version_from_natural_language():
    class Router:
        def route(self, *_):
            return [(TRACKING, "v2"), (TRACKING, "v2.10.4")], ""

    plan = source_plan(Settings(mock=False), Router(), "把 V2.10.4 的埋点定义同步进图谱。", {}, {}, {})
    assert plan.sources == [(TRACKING, "V2.10.4")]
    exact = source_plan(Settings(mock=False), Router(), "导入两个埋点版本 v2.10.4 与 V2.10.4。", {}, {}, {})
    assert exact.sources == [(TRACKING, "v2.10.4")]


def test_mock_tracking_routes_dates_and_explicit_aliases_without_guessing():
    mode = Settings(mock=True)
    date_plan = source_plan(mode, None, "帮我同步 2024.02.03 的埋点定义。", {}, {}, {})
    label_plan = source_plan(mode, None, "同步埋点版本 Release_S1", {}, {}, {})
    question_plan = source_plan(mode, None, "What is the tracking config?", {}, {}, {})
    assert date_plan.sources == [(TRACKING, "2024.02.03")]
    assert label_plan.sources == [(TRACKING, "Release_S1")]
    assert question_plan.sources == []


def test_json_mode_retries_one_blank_completion(monkeypatch):
    model = DeepSeek(Settings())
    responses = iter(["   ", '{"columns": []}'])
    calls = []

    def request(prompt, inputs, **kwargs):
        calls.append((prompt, kwargs))
        return SimpleNamespace(content=next(responses))

    monkeypatch.setattr(model, "request", request)
    assert model.complete("Return a JSON object", {}) == {"columns": []}
    assert len(calls) == 2 and all(options["json_mode"] is True for _, options in calls)
