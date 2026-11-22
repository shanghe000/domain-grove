from io import BytesIO

import pytest
from openpyxl import load_workbook

from duckdb_sandbox import ConfigDatabase
from settings import WorkerError

CONFIG_A = "cfg_rev_" + "a" * 32
CONFIG_B = "cfg_rev_" + "b" * 32
HIDDEN = "cfg_rev_" + "c" * 32
ACTIVITY_SCHEMA = {
    "table_name": "cfg_activity",
    "primary_key": "activity_id",
    "columns": [
        {"name": "activity_id", "duckdb_type": "BIGINT", "comment": "活动 ID"},
        {"name": "reward_group_id", "duckdb_type": "BIGINT", "comment": "奖励组"},
    ],
}
REWARD_SCHEMA = {
    "table_name": "cfg_reward",
    "primary_key": "reward_group_id",
    "columns": [
        {"name": "reward_group_id", "duckdb_type": "BIGINT"},
        {"name": "display", "duckdb_type": "VARCHAR"},
    ],
}


@pytest.fixture
def fixture(tmp_path):
    database = ConfigDatabase(tmp_path / "sandbox.duckdb")
    database.write_tables([
        (CONFIG_A, ACTIVITY_SCHEMA, [[key, 10] for key in range(1, 251)]),
        (CONFIG_B, REWARD_SCHEMA, [[10, "钻石"], [11, "=1+1"]]),
        (HIDDEN, REWARD_SCHEMA, [[10, "暂存修订"]]),
    ])
    artifacts = [
        {"physical_ref": CONFIG_A, "schema_meta": ACTIVITY_SCHEMA},
        {"physical_ref": CONFIG_B, "schema_meta": REWARD_SCHEMA},
    ]
    return database, artifacts


@pytest.mark.parametrize(
    "statement",
    [
        "DROP TABLE cfg_activity",
        "DELETE FROM cfg_activity",
        "COPY cfg_activity TO '/tmp/untrusted.csv'",
        "ATTACH '/tmp/unknown.duckdb'",
        "SELECT activity_id FROM cfg_activity; DELETE FROM cfg_activity",
        "SELECT * FROM read_csv('/tmp/untrusted.csv')",
        "SELECT current_setting('home_directory')",
        "SELECT * FROM '/tmp/untrusted.csv'",
        f"SELECT * FROM {HIDDEN}",
        f"SELECT * FROM main.{HIDDEN}",
        f"SELECT * FROM query_table('{HIDDEN}')",
        "WITH a AS (SELECT activity_id FROM cfg_activity) SELECT * FROM duckdb_tables()",
    ],
)
def test_reject_side_effects_and_unpublished_sources(fixture, statement):
    database, artifacts = fixture
    result = database.query(statement, artifacts)
    assert result["error"]
    assert result["hint_tables"][0]["schema_meta"] == ACTIVITY_SCHEMA
    assert database.query("SELECT count(*) AS total FROM cfg_activity", artifacts)["rows"] == [[250]]


def test_allow_cross_table_join_and_cte(fixture):
    database, artifacts = fixture
    result = database.query(
        "WITH Selected AS (SELECT * FROM cfg_Activity WHERE activity_id = 2) "
        "SELECT selected.activity_id, reward.display FROM Selected JOIN cfg_reward AS reward USING (reward_group_id)",
        artifacts,
    )
    assert result == {"columns": ["activity_id", "display"], "rows": [[2, "钻石"]], "truncated": False}


def test_result_limit_reports_real_truncation(fixture):
    database, artifacts = fixture
    clipped = database.query("SELECT activity_id FROM cfg_activity ORDER BY activity_id LIMIT 500", artifacts)
    exactly = database.query("SELECT activity_id FROM cfg_activity ORDER BY activity_id LIMIT 200", artifacts)
    unlimited = database.query("SELECT activity_id FROM cfg_activity WHERE activity_id <= 200", artifacts)
    assert len(clipped["rows"]) == len(exactly["rows"]) == len(unlimited["rows"]) == 200
    assert clipped["truncated"] is True
    assert exactly["truncated"] is unlimited["truncated"] is False


def test_preserves_computed_limits_and_reports_invalid_limits(fixture):
    database, artifacts = fixture
    prefix = "SELECT activity_id FROM cfg_activity ORDER BY activity_id"
    computed = database.query(prefix + " LIMIT 1 + 1", artifacts)
    invalid = database.query(prefix + " LIMIT -1", artifacts)
    assert computed["rows"] == [[1], [2]]
    assert computed["truncated"] is False
    assert invalid["error"] and invalid["hint_tables"]


def test_queries_do_not_see_a_partial_publish(fixture):
    database, artifacts = fixture
    first, failed = "cfg_rev_" + "d" * 32, "cfg_rev_" + "e" * 32
    with pytest.raises(WorkerError):
        database.write_tables([
            (first, ACTIVITY_SCHEMA, [[1, 10]]),
            (failed, REWARD_SCHEMA, [["不是数字", "值"]]),
        ])
    pending = [{**artifacts[0], "physical_ref": first}]
    assert database.query("SELECT * FROM cfg_activity", pending)["error"]
    database.write_tables([(first, ACTIVITY_SCHEMA, [[2, 10]]), (first, ACTIVITY_SCHEMA, [[9, 10]])])
    assert database.query("SELECT activity_id FROM cfg_activity", pending)["rows"] == [[2]]


def test_export_preserves_excel_literal_strings(fixture):
    database, artifacts = fixture
    workbook = load_workbook(BytesIO(database.export(artifacts[1])), read_only=True)
    assert list(workbook.active.values)[2] == (11, "=1+1")
    assert workbook.active["B3"].data_type == "s"


def test_lock_wait_counts_towards_query_deadline(fixture, monkeypatch):
    database, artifacts = fixture
    monkeypatch.setattr("duckdb_sandbox._QUERY_SECONDS", 0.025)
    with database._lock:
        assert "超过 3 秒" in database.query("SELECT 1", artifacts)["error"]


def test_deadline_interrupts_expensive_sql(fixture, monkeypatch):
    database, artifacts = fixture
    monkeypatch.setattr("duckdb_sandbox._QUERY_SECONDS", 0.1)
    result = database.query(
        "SELECT sum(a.activity_id*b.activity_id*c.activity_id*d.activity_id*e.activity_id) "
        "FROM cfg_activity a CROSS JOIN cfg_activity b CROSS JOIN cfg_activity c "
        "CROSS JOIN cfg_activity d CROSS JOIN cfg_activity e",
        artifacts,
    )
    assert "超过 3 秒" in result["error"]
