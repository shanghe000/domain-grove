from __future__ import annotations

import base64
import io
import math
import re
import threading
import time
from datetime import date, datetime, time as clock_time
from decimal import Decimal
from pathlib import Path

import duckdb
import sqlglot
from openpyxl import Workbook
from openpyxl.cell import WriteOnlyCell
from sqlglot import exp
from sqlglot.errors import SqlglotError
from sqlglot.optimizer.scope import Scope, traverse_scope

from settings import PHYSICAL_TABLE, TABLE, WorkerError

_ROW_LIMIT = 200
_QUERY_SECONDS = 3.0
_TIMEOUT = "配置查询超过 3 秒，请缩小查询范围后重试。"
_OPTIONS = {
    "enable_external_access": "false",
    "allow_unsigned_extensions": "false",
    "autoinstall_known_extensions": "false",
    "autoload_known_extensions": "false",
    "lock_configuration": "true",
    "memory_limit": "256MB",
    "max_temp_directory_size": "0GB",
    "threads": "2",
}
_ALLOWED_COLUMN_TYPES = {
    "VARCHAR", "BOOLEAN", "TINYINT", "SMALLINT", "INTEGER", "INT", "BIGINT", "HUGEINT",
    "FLOAT", "REAL", "DOUBLE", "DATE", "TIME", "TIMESTAMP", "JSON",
}
_DECIMAL = re.compile(r"DECIMAL\((\d{1,2}),\s*(\d{1,2})\)\Z")
_FUNCTIONS = frozenset("""
    ABS ACOS ANY_VALUE APPROX_COUNT_DISTINCT ARRAY_AGG ARRAY_CONCAT ARRAY_CONTAINS
    ARRAY_LENGTH ARRAY_SIZE ARRAY_TO_STRING ASIN ATAN ATAN2 AVG BIGINT BIT_AND BIT_OR
    BOOL_AND BOOL_OR CEIL CEILING CHAR_LENGTH COALESCE CONCAT CONCAT_WS CONTAINS CORR
    COS COUNT COUNT_IF COVAR_POP COVAR_SAMP CURRENT_DATE CURRENT_TIMESTAMP DATE
    DATE_ADD DATE_DIFF DATE_PART DATE_SUB DATE_TRUNC DATETIME DAY DENSE_RANK DOT_PRODUCT
    ENDS_WITH EXP EXTRACT FIRST FIRST_VALUE FLOOR GREATEST GROUP_CONCAT HOUR IF
    ILIKE INITCAP INSTR INTEGER JSON_ARRAY_LENGTH JSON_EXTRACT JSON_EXTRACT_SCALAR
    JSON_EXTRACT_STRING JSON_TYPE JSON_VALID LAG LAST LAST_VALUE LEAD LEAST LEFT
    LENGTH LIST LIST_AGG LIST_CONTAINS LN LOG LOG10 LOWER LPAD LTRIM MAKE_DATE
    MAX MEDIAN MIN MINUTE MOD MONTH NTH_VALUE NTILE NULLIF OCTET_LENGTH POSITION
    POWER QUANTILE QUANTILE_CONT QUANTILE_DISC RANK REGEXP_EXTRACT REGEXP_LIKE
    REGEXP_MATCHES REGEXP_REPLACE REPEAT REPLACE REVERSE RIGHT ROUND ROW_NUMBER
    RPAD RTRIM SECOND SHA256 SIGN SIN SPLIT SQRT STARTS_WITH STDDEV STDDEV_POP
    STDDEV_SAMP STRING_AGG STRING_SPLIT STRFTIME STRPOS STRPTIME SUBSTR SUBSTRING
    SUM TAN TIME TIMESTAMP TIMESTAMP_TRUNC TRANSLATE TRIM TRY_STRPTIME UPPER
    VARIANCE VAR_POP VAR_SAMP WEEK YEAR
""".split())
_FORBIDDEN = (
    exp.DDL, exp.DML, exp.Command, exp.Into, exp.Parameter, exp.Placeholder,
    exp.Attach, exp.Detach, exp.Pragma, exp.Copy, exp.Execute, exp.Set, exp.Lock,
)
_SPECIAL_SYNTAX = (exp.Cast, exp.TryCast, exp.Case, exp.Filter)


def _identifier(name: str) -> str:
    if not isinstance(name, str) or not name or len(name) > 128 or "\0" in name:
        raise ValueError("列名或表名无效。")
    return '"' + name.replace('"', '""') + '"'


def _column_type(name: str) -> str:
    name = str(name).upper()
    if name in _ALLOWED_COLUMN_TYPES:
        return name
    decimal = _DECIMAL.fullmatch(name)
    if decimal:
        precision, scale = (int(value) for value in decimal.groups())
        if 1 <= precision <= 38 and 0 <= scale <= precision:
            return f"DECIMAL({precision},{scale})"
    raise ValueError("配置表存在不支持的列类型。")


def _logical_tables(artifacts: list[dict]) -> tuple[dict[str, str], list[dict]]:
    tables: dict[str, str] = {}
    hints: list[dict] = []
    for artifact in artifacts:
        meta = artifact.get("schema_meta") or {}
        physical = artifact.get("physical_ref") or ""
        logical = meta.get("table_name") or ""
        if not PHYSICAL_TABLE.fullmatch(physical) or not TABLE.fullmatch(logical) or PHYSICAL_TABLE.fullmatch(logical):
            continue
        tables[logical] = physical
        hints.append({"table_name": logical, "schema_meta": meta})
    return tables, sorted(hints, key=lambda hint: hint["table_name"])


def _checked_select(sql: str, tables: dict[str, str]) -> tuple[str, set[str]]:
    if not isinstance(sql, str) or not sql.strip() or len(sql) > 20_000:
        raise ValueError("请输入不超过 20,000 字符的 SQL 查询。")
    statements = [item for item in sqlglot.parse(sql, dialect="duckdb") if item is not None]
    if len(statements) != 1 or not isinstance(statements[0], exp.Select):
        raise ValueError("仅允许单条 SELECT 或 WITH … SELECT 查询。")
    statement = statements[0]
    for node in statement.walk():
        if isinstance(node, _FORBIDDEN):
            raise ValueError("查询含有不受支持的语句。")
        if isinstance(node, exp.Table) and (
            not isinstance(node.this, exp.Identifier) or node.db or node.catalog
        ):
            raise ValueError("仅允许使用已发布配置表、CTE 与子查询。")
        if isinstance(node, exp.Func) and not isinstance(node, _SPECIAL_SYNTAX):
            name = node.name if isinstance(node, exp.Anonymous) else node.sql_name()
            if name.upper() not in _FUNCTIONS:
                raise ValueError(f"查询使用了不受支持的函数 {name.upper()}。")

    sources: set[str] = set()
    for scope in traverse_scope(statement):
        if not isinstance(scope.expression, (exp.Select, exp.SetOperation)):
            raise ValueError("子查询须使用 SELECT。")
        for _, source in scope.selected_sources.values():
            if isinstance(source, Scope):
                continue
            if not isinstance(source, exp.Table):
                raise ValueError("查询含有不受支持的数据来源。")
            name = source.name.lower()
            if name not in tables:
                raise ValueError(f"没有已发布的配置表 {name}。")
            sources.add(name)

    limit = statement.args.get("limit")
    # Fetch one extra row to distinguish a clipped result from an exact 200-row result.
    cap = exp.Literal.number(_ROW_LIMIT + 1)
    if limit is None:
        statement.set("limit", exp.Limit(expression=cap))
    elif isinstance(limit, exp.Limit) and not limit.args.get("limit_options"):
        value = limit.expression
        if isinstance(value, exp.Literal) and value.is_int and int(value.this) >= 0:
            if int(value.this) > _ROW_LIMIT:
                limit.set("expression", cap)
        elif (value is None or isinstance(value, exp.Null)
              or (isinstance(value, exp.Var) and value.name.upper() == "ALL")):
            limit.set("expression", cap)
        else:
            # Retain smaller computed limits and let DuckDB diagnose invalid limits.
            limit.set("expression", exp.Least(this=cap, expressions=[value]))
    else:
        statement = exp.select("*").from_(statement.subquery("_bounded_config_query")).limit(_ROW_LIMIT + 1)
    return statement.sql(dialect="duckdb"), sources


def _json_value(value):
    if isinstance(value, (datetime, date, clock_time)):
        return value.isoformat()
    if isinstance(value, Decimal):
        return str(value)
    if isinstance(value, bytes):
        return base64.b64encode(value).decode("ascii")
    if isinstance(value, float) and not math.isfinite(value):
        return None
    if isinstance(value, (list, tuple)):
        return [_json_value(item) for item in value]
    if isinstance(value, dict):
        return {str(key): _json_value(item) for key, item in value.items()}
    if value is None or isinstance(value, (bool, int, float, str)):
        return value
    return str(value)


class ConfigDatabase:
    def __init__(self, path: Path):
        self.path = path
        self._lock = threading.Lock()

    def write_tables(self, updates: list[tuple[str, dict, list[list]]], *, replace: bool = False):
        if not updates:
            return
        self.path.parent.mkdir(parents=True, exist_ok=True, mode=0o750)
        try:
            with self._lock, duckdb.connect(str(self.path), config=_OPTIONS) as connection:
                connection.execute("BEGIN TRANSACTION")
                try:
                    for physical, meta, rows in updates:
                        self._write_table(connection, physical, meta, rows, replace)
                    connection.execute("COMMIT")
                except Exception:
                    connection.execute("ROLLBACK")
                    raise
        except (duckdb.Error, ValueError, KeyError, TypeError) as exc:
            raise WorkerError("配置表无法入库，请核对表头类型及数据行。") from exc

    def _write_table(self, connection, physical: str, meta: dict, rows: list[list], replace: bool):
        if not PHYSICAL_TABLE.fullmatch(physical):
            raise ValueError("配置表版本标识无效。")
        table = _identifier(physical)
        columns = meta["columns"]
        names = [_identifier(col["name"]) for col in columns]
        if not names or len(names) != len(set(name.lower() for name in names)):
            raise ValueError("配置表列为空或重复。")
        definitions = [f'{name} {_column_type(column["duckdb_type"])}' for name, column in zip(names, columns)]
        exists = connection.execute(
            "SELECT count(*) FROM information_schema.tables WHERE table_schema = 'main' AND table_name = ?",
            [physical],
        ).fetchone()[0]
        if exists:
            if not replace:
                return
            connection.execute(f"DELETE FROM main.{table}")
        else:
            if replace:
                raise ValueError("尚未找到要热更的已发布表。")
            connection.execute(f"CREATE TABLE main.{table} ({', '.join(definitions)})")
        placeholders = "(" + ", ".join("?" for _ in names) + ")"
        chunk_size = max(8, 800 // len(names))
        for offset in range(0, len(rows), chunk_size):
            chunk = rows[offset:offset + chunk_size]
            values = ", ".join([placeholders] * len(chunk))
            params = [value for row in chunk for value in row]
            connection.execute(f"INSERT INTO main.{table} VALUES {values}", params)

    def query(self, sql: str, artifacts: list[dict]) -> dict:
        started = time.monotonic()
        tables, hints = _logical_tables(artifacts)

        def failure(message: str) -> dict:
            return {"error": message, "hint_tables": hints}

        try:
            statement, sources = _checked_select(sql, tables)
        except (SqlglotError, RecursionError):
            return failure("SQL 语法无法解析，请核对 SELECT 语句。")
        except ValueError as exc:
            return failure(str(exc))
        if not self.path.is_file():
            return failure("暂无已发布的配置表版本。")

        remaining = _QUERY_SECONDS - (time.monotonic() - started)
        if remaining <= 0 or not self._lock.acquire(timeout=remaining):
            return failure(_TIMEOUT)
        try:
            with duckdb.connect(str(self.path), read_only=True, config=_OPTIONS) as connection:
                return self._query_connection(connection, statement, sources, tables, started, failure)
        except duckdb.Error:
            return failure("配置库暂时无法读取。")
        finally:
            self._lock.release()

    def _query_connection(self, connection, statement, sources, tables, started, failure) -> dict:
        expired = threading.Event()

        def interrupt():
            expired.set()
            connection.interrupt()

        remaining = _QUERY_SECONDS - (time.monotonic() - started)
        if remaining <= 0:
            return failure(_TIMEOUT)
        timer = threading.Timer(remaining, interrupt)
        timer.start()
        try:
            for name in sorted(sources):
                # A TEMP view exists only on this read-only connection.
                connection.execute(
                    f"CREATE TEMP VIEW {_identifier(name)} AS SELECT * FROM main.{_identifier(tables[name])}"
                )
            cursor = connection.execute(statement)
            columns = [column[0] for column in cursor.description]
            rows = cursor.fetchmany(_ROW_LIMIT + 1)
        except duckdb.Error as exc:
            if expired.is_set() or isinstance(exc, duckdb.InterruptException):
                return failure(_TIMEOUT)
            if isinstance(exc, (duckdb.BinderException, duckdb.ConversionException, duckdb.ParserException)):
                return failure(str(exc)[:450])
            return failure("查询无法执行，请核对配置表或缩小查询范围。")
        finally:
            timer.cancel()
            timer.join()
        if expired.is_set() or time.monotonic() - started >= _QUERY_SECONDS:
            return failure(_TIMEOUT)
        return {
            "columns": columns,
            "rows": [[_json_value(value) for value in row] for row in rows[:_ROW_LIMIT]],
            "truncated": len(rows) > _ROW_LIMIT,
        }

    def export(self, artifact: dict) -> bytes:
        physical = artifact.get("physical_ref") or ""
        logical = (artifact.get("schema_meta") or {}).get("table_name") or ""
        if not PHYSICAL_TABLE.fullmatch(physical) or not TABLE.fullmatch(logical) or not self.path.is_file():
            raise WorkerError("未找到已发布的配置表版本。", 404)
        try:
            with self._lock, duckdb.connect(str(self.path), read_only=True, config=_OPTIONS) as connection:
                cursor = connection.execute(f"SELECT * FROM main.{_identifier(physical)}")
                workbook = Workbook(write_only=True)
                sheet = workbook.create_sheet(title=logical[:31])
                sheet.append([self._literal_cell(sheet, column[0]) for column in cursor.description])
                while rows := cursor.fetchmany(1024):
                    for row in rows:
                        sheet.append([self._literal_cell(sheet, value) for value in row])
                output = io.BytesIO()
                workbook.save(output)
                return output.getvalue()
        except duckdb.Error as exc:
            raise WorkerError("配置表暂时无法下载。", 502) from exc

    @staticmethod
    def _literal_cell(sheet, value):
        if not isinstance(value, str):
            return value
        cell = WriteOnlyCell(sheet, value=value)
        cell.data_type = "s"
        return cell
