import base64
import io
import json

from fastapi.testclient import TestClient
from openpyxl import Workbook, load_workbook

from main import create_app
from settings import Settings


def xlsx(name, rows):
    data = io.BytesIO()
    book = Workbook()
    for row in rows:
        book.active.append(row)
    book.save(data)
    return {"name": name, "content_base64": base64.b64encode(data.getvalue()).decode()}


def client_at(tmp_path, *, mock=True, token=""):
    return TestClient(create_app(Settings(data_dir=tmp_path, duckdb_path=tmp_path / "test.duckdb",
                                          mock=mock, worker_token=token, llm_key="")))


def post(client, path, payload):
    response = client.post(path, json=payload)
    assert response.status_code == 200, response.text
    return response.json()


def chat(client, session, *, files=None, message="", artifacts=None, skeleton=None):
    return post(client, "/chat", {"session_id": session, "message": message, "uploaded_files": files,
                                "artifacts": artifacts, "skeleton": skeleton})


def publish(client, session, result):
    return post(client, "/commit", {"session_id": session, "artifacts": result["draft_patch"]["artifacts"],
                                    "staging_payload": result["staging_payload"]})["artifacts"]


def test_same_schema_updates_values_and_changed_fk_creates_draft(tmp_path):
    client = client_at(tmp_path)
    files = [
        xlsx("Reward.xlsx", [["reward_id", "item"], ["奖励编号", "奖励"], [10, "金币"], [20, "体力"]]),
        xlsx("Activity.xlsx", [["活动配置", None], ["activity_id", "amount"], ["活动编号", "数量"],
                               ["BIGINT", "BIGINT"], [101, 30], [102, 55]]),
    ]
    first = chat(client, "original", files=files)
    assert len(first["draft_patch"]["artifacts"]) == 2
    assert len(first["staging_payload"]["sources"]) == 2
    activity = next(a for a in first["draft_patch"]["artifacts"] if a["node_id"] == "Artifact_cfg_activity")
    assert activity["schema_meta"]["header_data_start_row"] == 5
    assert [col["name"] for col in activity["schema_meta"]["columns"]] == ["activity_id", "amount"]
    assert post(client, "/query_config_sql", {"sql": "SELECT amount FROM cfg_activity", "artifacts": []}).get("error")
    artifacts = publish(client, "original", first)

    quantities = "SELECT amount FROM cfg_activity ORDER BY activity_id"
    assert post(client, "/query_config_sql", {"sql": quantities, "artifacts": artifacts})["rows"] == [[30], [55]]
    changes = xlsx("Activity.xlsx", [["activity_id", "amount"], ["活动编号", "数量"], [101, 99], [102, 55]])
    hot = chat(client, "hot", files=[changes], artifacts=artifacts)
    assert hot["draft_patch"]["artifacts"] == []
    assert hot["staging_payload"]["sources"] == []
    assert hot["hot_updates"] == [{"node_id": "Artifact_cfg_activity", "table_name": "cfg_activity"}]
    assert post(client, "/query_config_sql", {"sql": quantities, "artifacts": artifacts})["rows"] == [[99], [55]]

    changes = xlsx("Activity.xlsx", [["activity_id", "amount", "reward_id"], ["活动编号", "数量", "外键 cfg_reward.reward_id"],
                                     [101, 30, 10], [102, 55, 20]])
    draft = chat(client, "schema", files=[changes], artifacts=artifacts)
    tracked = draft["draft_patch"]["artifacts"]
    assert [item["node_id"] for item in tracked] == ["Artifact_cfg_activity"]
    relation = next(e for e in draft["draft_patch"]["edges"] if e["predicate"] == "foreign_key_to")
    assert relation["metadata"]["join_clause"] == "cfg_activity.reward_id = cfg_reward.reward_id"
    column = tracked[0]["schema_meta"]["columns"][-1]
    column["comment"], column["confidence"] = "已审核的外键", "high"
    assert post(client, "/query_config_sql", {"sql": quantities, "artifacts": artifacts})["rows"] == [[99], [55]]
    changed = publish(client, "schema", draft)[0]
    repeat = publish(client, "schema", draft)[0]
    assert repeat["physical_ref"] == changed["physical_ref"]
    assert changed["schema_meta"]["columns"][-1]["comment"] == "已审核的外键"
    old_activity = next(item for item in artifacts if item["node_id"] == changed["node_id"])
    assert old_activity["physical_ref"] != changed["physical_ref"]
    reward = next(item for item in artifacts if item["node_id"] == "Artifact_cfg_reward")
    joined = post(client, "/query_config_sql", {"sql": "SELECT a.activity_id,r.item FROM cfg_activity a "
                  "JOIN cfg_reward r ON a.reward_id=r.reward_id ORDER BY a.activity_id", "artifacts": [reward, changed]})
    assert joined["rows"] == [[101, "金币"], [102, "体力"]]
    workbook = client.post("/download_config", json={"artifact": changed})
    assert workbook.status_code == 200, workbook.text
    copied = load_workbook(io.BytesIO(workbook.content), read_only=True)
    assert next(copied.active.values) == ("activity_id", "amount", "reward_id")
    copied.close()


def test_stream_routes_mixed_sources_and_publishes_full_text(tmp_path):
    client = client_at(tmp_path)
    outline = {"nodes": [
        {"id": "Domain_LiveOps", "label": "运营", "node_type": "Domain"},
        {"id": "Domain_BI", "label": "BI", "node_type": "Domain"},
        {"id": "Metric_Retention", "label": "留存", "node_type": "Metric"},
    ]}
    response = client.post("/chat/stream", json={"session_id": "mixed", "skeleton": outline, "message":
        "https://tenant.feishu.cn/wiki/DemoNode https://demo.invalid/download?name=ReadingChallenge.xlsx 同步埋点版本 v1.2.0"})
    assert response.status_code == 200, response.text
    lines = [json.loads(line) for line in response.text.splitlines()]
    assert {line.get("tool") for line in lines if line.get("event") == "progress"} == {
        "route", "fetch_and_model_feishu_doc", "fetch_and_model_config_table", "sync_and_model_tracking_version"}
    assert lines[-1]["event"] == "result", lines[-1]
    result = lines[-1]["result"]
    binding = next(b for b in result["draft_patch"]["artifacts"] if b["artifact_type"] == "feishu_md")
    assert len(result["staging_payload"]["sources"]) == 3
    assert not (tmp_path / binding["physical_ref"]).exists()
    wrong = client.post("/commit", json={"session_id": "other", "artifacts": [binding],
                                        "staging_payload": result["staging_payload"]})
    assert wrong.status_code == 409
    artifacts = publish(client, "mixed", result)
    doc = next(item for item in artifacts if item["artifact_type"] == "feishu_md")
    path = tmp_path / "docs" / "revisions" / doc["node_id"] / (doc["schema_meta"]["content_revision"] + ".md")
    text = path.read_text()
    assert text.startswith("---\nnode_id:")
    assert "> [!NOTE]" in text and "| 条件 | 徽章代码 |" in text
    tracking = post(client, "/query_tracking_events", {"version":"v1.2.0","event_names":["reading_session_start"]})
    assert [event["name"] for event in tracking["events"]] == ["reading_session_start"]


def test_internal_token_and_live_missing_configuration(tmp_path):
    client = client_at(tmp_path, mock=False, token="test-worker-token")
    assert client.get("/health").json()["mock"] is False
    assert client.post("/chat", json={"session_id": "live"}).status_code == 401
    response = client.post("/chat", headers={"Authorization": "Bearer test-worker-token"},
                           json={"session_id": "live", "message": "读取飞书策划案"})
    assert response.status_code == 503 and "LLM_API_KEY" in response.json()["error"]


def test_same_old_workbook_republish_does_not_reuse_hot_revision(tmp_path):
    client = client_at(tmp_path)
    original = xlsx("Activity.xlsx", [["activity_id", "amount"], [101, 30]])
    updated = xlsx("Activity.xlsx", [["activity_id", "amount"], [101, 99]])
    new_schema = xlsx("Activity.xlsx", [["activity_id", "amount", "kind"], [101, 45, "x"]])

    alpha = publish(client, "a", chat(client, "a", files=[original]))
    chat(client, "hot", files=[updated], artifacts=alpha)
    beta = publish(client, "b", chat(client, "b", files=[new_schema], artifacts=alpha))
    reverted = publish(client, "a2", chat(client, "a2", files=[original], artifacts=beta))

    assert alpha[0]["physical_ref"] != reverted[0]["physical_ref"]
    assert post(client, "/query_config_sql", {"sql": "SELECT amount FROM cfg_activity",
                                             "artifacts": reverted})["rows"] == [[30]]
