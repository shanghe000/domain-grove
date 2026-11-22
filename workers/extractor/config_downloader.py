from __future__ import annotations

import base64
import binascii
from email.message import Message
import io
from pathlib import PurePosixPath
import ssl
from urllib.parse import parse_qs, unquote, urlencode, urlunsplit
from uuid import UUID

from openpyxl import Workbook

from settings import Settings, WorkerError, endpoint, fetch, public_url, split_url

MAX_FILE = 8 * 1024 * 1024
CONFIG_ENVIRONMENTS = {"development": "development", "dev": "development", "master": "master", "prod": "master"}
SOURCE_PARAMS = ("name", "filename", "id", "version", "project_id", "project_code", "env")


def file_name(value: str) -> str:
    if (not isinstance(value, str) or not value or len(value) > 160 or "/" in value or "\\" in value
            or any(ord(character) < 32 for character in value) or not value.lower().endswith(".xlsx")):
        raise WorkerError("请提供有效的 .xlsx 文件名。")
    return value


def uploaded(item: dict) -> tuple[str, bytes, str]:
    name = file_name(item.get("name") or item.get("filename") or "")
    value = item.get("content_base64") or ""
    if not isinstance(value, str) or len(value) > (MAX_FILE * 4 // 3 + 4):
        raise WorkerError("单个工作簿最大 8 MiB。", 413)
    try:
        data = base64.b64decode(value, validate=True)
    except (ValueError, binascii.Error) as exc:
        raise WorkerError("工作簿传输内容无效。") from exc
    if not data or len(data) > MAX_FILE:
        raise WorkerError("请上传 8 MiB 以内的非空工作簿。", 413)
    return name, data, ""


def same_origin(a, b) -> bool:
    def origin(url):
        return (url.scheme, url.hostname, url.port or (443 if url.scheme == "https" else 80))
    return origin(a) == origin(b)


def within(configured, url) -> bool:
    path = configured.path.rstrip("/")
    return same_origin(configured, url) and (url.path == path or url.path.startswith(path + "/"))


def name_from_url(link: str) -> str | None:
    url = split_url(link)
    values = parse_qs(url.query)
    return next((v for v in (values.get("name", [""])[0], values.get("filename", [""])[0],
                             unquote(PurePosixPath(url.path).name)) if v.lower().endswith(".xlsx")), None)


def config_candidate(settings: Settings, link: str) -> bool:
    try:
        url = split_url(link)
        if settings.config_base and within(split_url(settings.config_base), url):
            return True
        return name_from_url(link) is not None
    except WorkerError:
        return False


def mock_workbook(name: str) -> bytes:
    book = Workbook()
    sheet = book.active
    if "reward" in name.lower():
        for row in [["reward_id", "item_name"], ["奖励编号", "道具名称"], [10, "金币"], [20, "体力"]]:
            sheet.append(row)
    else:
        for row in [["activity_id", "reward_id", "amount"], ["活动编号", "外键 cfg_reward.reward_id", "发放数量"],
                    [101, 10, 30], [102, 20, 50]]:
            sheet.append(row)
    buffer = io.BytesIO()
    book.save(buffer)
    return buffer.getvalue()


def download(settings: Settings, link: str) -> tuple[str, bytes, str]:
    url = split_url(link)
    name = file_name(name_from_url(link) or "Config.xlsx")
    display = public_url(link, SOURCE_PARAMS)
    if settings.mock:
        return name, mock_workbook(name), display
    base = endpoint(settings.config_base, "CONFIG_DOWNLOAD_API_BASE_URL")
    decoded_path = unquote(url.path)
    if (not within(base, url) or "\\" in decoded_path or unquote(decoded_path) != decoded_path
            or has_encoded_separator(url.path) or any(p in {".", ".."} for p in decoded_path.split("/"))):
        raise WorkerError("配置下载链接不在允许运行的服务路径内。")
    if settings.config_project_id or settings.config_environment:
        display = pinned_source_url(settings, url)
    headers = {"Authorization": f"Bearer {settings.config_token}"} if settings.config_token else {}
    response_headers: dict[str, str] = {}
    data = fetch(link, service="配置下载服务", limit=MAX_FILE, headers=headers,
                 verify=config_trust(settings.config_ca_file), response_headers=response_headers)
    if disposition := response_headers.get("content-disposition"):
        message = Message()
        message["Content-Disposition"] = disposition
        if filename := message.get_filename():
            name = file_name(filename)
    return name, data, display


def pinned_source_url(settings: Settings, url) -> str:
    try:
        expected_project = UUID(settings.config_project_id)
        expected_environment = CONFIG_ENVIRONMENTS[settings.config_environment]
    except (ValueError, KeyError) as exc:
        raise WorkerError("请同时配置有效的配置下载项目 UUID 和环境。", 503) from exc
    params = parse_qs(url.query, keep_blank_values=True)
    try:
        (project,) = params["project_id"]
        (environment,) = params["env"]
        approved = UUID(project) == expected_project and CONFIG_ENVIRONMENTS[environment] == expected_environment
    except (ValueError, KeyError):
        approved = False
    if not approved or "project_code" in params:
        raise WorkerError("配置下载链接的项目或环境不在批准范围内。", 403)
    query = urlencode({"project_id": str(expected_project), "env": expected_environment})
    return urlunsplit((url.scheme, url.netloc, url.path, query, ""))


def config_trust(ca_file: str) -> ssl.SSLContext | bool:
    if not ca_file:
        return True
    try:
        return ssl.create_default_context(cafile=ca_file)
    except OSError as exc:
        raise WorkerError("配置下载服务的信任证书不可用。", 503) from exc


def has_encoded_separator(path: str) -> bool:
    low = path.lower()
    return "%2f" in low or "%5c" in low
