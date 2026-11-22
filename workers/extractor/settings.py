from __future__ import annotations

import json
import os
import re
import ssl
import tempfile
import time
from contextvars import ContextVar
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path
from urllib.parse import parse_qsl, urlencode, urlsplit, urlunsplit

import httpx


class WorkerError(Exception):
    def __init__(self, message: str, status: int = 422):
        super().__init__(message)
        self.status = status


def env(name: str, default: str = "") -> str:
    return os.getenv(name, default).strip()


@dataclass(frozen=True)
class Settings:
    data_dir: Path = field(default_factory=lambda: Path(env("DATA_DIR", "./data")).resolve())
    duckdb_path: Path | None = None
    mock: bool = field(default_factory=lambda: env("MOCK_EXTERNAL_APIS").lower() == "true")
    worker_token: str = field(default_factory=lambda: env("WORKER_API_TOKEN"), repr=False)
    llm_base: str = field(default_factory=lambda: env("LLM_BASE_URL", "https://api.deepseek.com/v1"))
    llm_model: str = field(default_factory=lambda: env("LLM_MODEL", "deepseek-flash"))
    llm_key: str = field(default_factory=lambda: env("LLM_API_KEY"), repr=False)
    feishu_base: str = field(default_factory=lambda: env("FEISHU_API_BASE_URL", "https://open.feishu.cn/open-apis"))
    feishu_app: str = field(default_factory=lambda: env("FEISHU_APP_ID"))
    feishu_secret: str = field(default_factory=lambda: env("FEISHU_APP_SECRET"), repr=False)
    feishu_hosts: tuple[str, ...] = field(default_factory=lambda: tuple(
        host.strip().lower() for host in env("FEISHU_HOSTS", "feishu.cn,larksuite.com").split(",") if host.strip()
    ))
    config_base: str = field(default_factory=lambda: env("CONFIG_DOWNLOAD_API_BASE_URL"))
    config_token: str = field(default_factory=lambda: env("CONFIG_DOWNLOAD_API_TOKEN"), repr=False)
    config_ca_file: str = field(default_factory=lambda: env("CONFIG_DOWNLOAD_CA_FILE"))
    config_project_id: str = field(default_factory=lambda: env("CONFIG_DOWNLOAD_PROJECT_ID"))
    config_environment: str = field(default_factory=lambda: env("CONFIG_DOWNLOAD_ENV"))
    tracking_base: str = field(default_factory=lambda: env("TRACKING_API_BASE_URL"))
    tracking_token: str = field(default_factory=lambda: env("TRACKING_API_TOKEN"), repr=False)

    @property
    def database(self) -> Path:
        return self.duckdb_path or Path(env("DUCKDB_PATH", str(self.data_dir / "configs.duckdb"))).resolve()


deadline: ContextVar[float] = ContextVar("deadline", default=float("inf"))


def remaining(maximum: float = 18) -> float:
    seconds = min(deadline.get() - time.monotonic(), maximum)
    if seconds <= 0:
        raise WorkerError("本次来源处理已超时，请减少一次投喂的资料数量后重试。", 504)
    return seconds


def http_timeout() -> httpx.Timeout:
    seconds = remaining()
    return httpx.Timeout(seconds, connect=min(seconds, 4))


def split_url(value: str):
    try:
        url = urlsplit(value)
        if url.scheme not in {"http", "https"} or not url.hostname or url.username or url.password:
            raise ValueError
        _ = url.port
    except (TypeError, ValueError) as exc:
        raise WorkerError("只接受不含账号和密码的 HTTP(S) 链接。") from exc
    return url


def endpoint(value: str, variable: str):
    if not value:
        raise WorkerError(f"尚未配置 {variable}。", 503)
    result = split_url(value)
    if result.scheme != "https" and result.hostname not in {"127.0.0.1", "::1", "localhost"}:
        raise WorkerError(f"{variable} 需要使用 HTTPS。", 503)
    if result.query or result.fragment:
        raise WorkerError(f"{variable} 不应包含查询参数或锚点。", 503)
    return result


def public_url(value: str, keep: tuple[str, ...] = ()) -> str:
    url = split_url(value)
    params = [(name, item) for name, item in parse_qsl(url.query) if name in keep and len(item) <= 200]
    return urlunsplit((url.scheme, url.netloc, url.path, urlencode(params), ""))


def fetch(url: str, *, service: str, limit: int, method: str = "GET", verify: ssl.SSLContext | bool = True,
          response_headers: dict[str, str] | None = None, **options) -> bytes:
    try:
        with httpx.Client(timeout=http_timeout(), follow_redirects=False, trust_env=False, verify=verify) as client:
            with client.stream(method, url, **options) as response:
                if response.status_code in (401, 403):
                    raise WorkerError(f"{service}拒绝访问，请核对应用授权及资料共享范围。", 403)
                if response.status_code == 404:
                    raise WorkerError(f"{service}未找到指定资料。", 404)
                response.raise_for_status()
                if response_headers is not None:
                    response_headers.update(response.headers)
                body = bytearray()
                for chunk in response.iter_bytes():
                    remaining()
                    body.extend(chunk)
                    if len(body) > limit:
                        raise WorkerError(f"{service}返回的资料超过处理上限。", 413)
                return bytes(body)
    except httpx.TimeoutException as exc:
        raise WorkerError(f"{service}响应超时。", 504) from exc
    except httpx.HTTPError as exc:
        raise WorkerError(f"{service}请求失败。", 502) from exc


def fetch_json(url: str, *, service: str, **options) -> dict:
    try:
        data = json.loads(fetch(url, service=service, limit=4_000_000, **options))
    except (ValueError, RecursionError) as exc:
        raise WorkerError(f"{service}返回的 JSON 无法读取。", 502) from exc
    if not isinstance(data, dict):
        raise WorkerError(f"{service}返回的数据格式不受支持。", 502)
    return data


def now() -> str:
    return datetime.now(timezone.utc).isoformat(timespec="seconds").replace("+00:00", "Z")


def write_atomic(path: Path, payload: bytes):
    path.parent.mkdir(parents=True, mode=0o750, exist_ok=True)
    descriptor, name = tempfile.mkstemp(dir=path.parent)
    try:
        with os.fdopen(descriptor, "wb") as stream:
            stream.write(payload)
            stream.flush()
            os.fchmod(stream.fileno(), 0o640)
            os.fsync(stream.fileno())
        os.replace(name, path)
    finally:
        if os.path.exists(name):
            os.unlink(name)


NODE_ID = re.compile(r"[A-Za-z0-9_][A-Za-z0-9_-]{0,127}\Z")
TABLE = re.compile(r"cfg_[a-z0-9_]{1,76}\Z")
PHYSICAL_TABLE = re.compile(r"cfg_rev_[0-9a-f]{32}\Z")
STAGE_ID = re.compile(r"[0-9a-f]{32}\Z")
