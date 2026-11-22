import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from threading import Thread

from deepseek_agent import DeepSeek, FEISHU
from settings import Settings


def test_openai_compatible_tool_call_and_json_output():
    requests = []
    source = "https://team.feishu.cn/docx/Fixture"

    class Endpoint(BaseHTTPRequestHandler):
        def log_message(self, *_args):
            pass

        def do_POST(self):
            data = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
            requests.append((self.path, self.headers["Authorization"], data))
            if data.get("tools"):
                message = {"role": "assistant", "content": None, "tool_calls": [{
                    "id": "call_fixture", "type": "function", "function": {
                        "name": FEISHU, "arguments": json.dumps({"url": source}),
                    },
                }]}
                finish = "tool_calls"
            else:
                message = {"role": "assistant", "content": '{"nodes": []}'}
                finish = "stop"
            response = json.dumps({"id": "fixture", "object": "chat.completion", "created": 1,
                                   "model": "deepseek-flash", "choices": [
                                       {"index": 0, "message": message, "finish_reason": finish},
                                   ]}).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(response)))
            self.end_headers()
            self.wfile.write(response)

    with ThreadingHTTPServer(("127.0.0.1", 0), Endpoint) as server:
        thread = Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            client = DeepSeek(Settings(llm_key="fixture-bearer", llm_model="deepseek-flash",
                                       llm_base=f"http://127.0.0.1:{server.server_port}/v1"))
            assert client.route(source, [source], {}, {}) == ([(FEISHU, source)], "")
            assert client.complete("Return JSON", {}) == {"nodes": []}
        finally:
            server.shutdown()
            thread.join()

    assert len(requests) == 2
    for path, authorization, body in requests:
        assert path == "/v1/chat/completions"
        assert authorization == "Bearer fixture-bearer"
        assert body["model"] == "deepseek-flash"
        assert body["thinking"] == {"type": "disabled"}
    assert requests[0][2]["tool_choice"] == "auto"
    assert requests[1][2]["response_format"] == {"type": "json_object"}
