from __future__ import annotations

import re

from settings import Settings, WorkerError, endpoint, fetch_json

VERSION = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]{0,63}\Z")


def tracking_definition(settings: Settings, version: str, event_names: list[str]) -> dict:
    if not VERSION.fullmatch(version):
        raise WorkerError("请输入有效的埋点版本，例如 v1.2.0。")
    if len(event_names) > 100 or any(not VERSION.fullmatch(name) for name in event_names):
        raise WorkerError("埋点筛选名称无效或超过 100 个。")
    if settings.mock:
        events = [
            {"name": "account_register", "description": "账号首次注册",
             "fields": [{"name": "account_id", "type": "string"}, {"name": "registered_at", "type": "timestamp"}]},
            {"name": "account_login", "description": "账号登录",
             "fields": [{"name": "account_id", "type": "string"}, {"name": "event_at", "type": "timestamp"}]},
            {"name": "reading_session_start", "description": "已登录读者开始阅读",
             "fields": [{"name": "account_id", "type": "string"}, {"name": "event_at", "type": "timestamp"}]},
        ]
    else:
        base = endpoint(settings.tracking_base, "TRACKING_API_BASE_URL")
        headers = {"Authorization": f"Bearer {settings.tracking_token}"} if settings.tracking_token else {}
        params = {"version": version}
        if event_names:
            params["event_names"] = ",".join(event_names)
        response = fetch_json(base.geturl(), service="数仓埋点服务", params=params, headers=headers)
        data = response.get("data", response)
        events = data.get("events") if isinstance(data, dict) else None
        if not isinstance(events, list):
            raise WorkerError("埋点服务未返回 events 数组。", 502)
    if event_names:
        needed = set(event_names)
        events = [e for e in events if isinstance(e, dict) and (e.get("name") or e.get("event_name")) in needed]
    return {"version": version, "events": events, "mock": settings.mock}
