from __future__ import annotations

import hmac
import json
import logging
from queue import Empty, Queue
from threading import BoundedSemaphore, Thread

from fastapi import Depends, FastAPI, Request
from fastapi.exceptions import RequestValidationError
from fastapi.responses import JSONResponse, Response, StreamingResponse
from pydantic import BaseModel, Field

from settings import Settings, WorkerError
from staging import Extractor
from tracking_sync import tracking_definition

log = logging.getLogger(__name__)
MAX_CHAT_SIZE = 2 * 8 * 1024 * 1024 * 4 // 3 + 1_000_000


class ChatRequest(BaseModel):
    session_id: str = Field(min_length=1, max_length=128)
    message: str = Field(default="", max_length=12_000)
    uploaded_files: list[dict] | None = Field(default=None, max_length=2)
    skeleton: dict | None = None
    draft_patch: dict | None = None
    artifacts: list[dict] | None = None


class CommitRequest(BaseModel):
    session_id: str = Field(min_length=1, max_length=128)
    artifacts: list[dict] | None = None
    staging_payload: dict | None = None


class SQLRequest(BaseModel):
    sql: str = Field(min_length=1, max_length=16_000)
    artifacts: list[dict] | None = None


class TrackingRequest(BaseModel):
    version: str = Field(min_length=1, max_length=64)
    event_names: list[str] | None = Field(default=None, max_length=100)


class DownloadRequest(BaseModel):
    artifact: dict


def create_app(settings: Settings | None = None) -> FastAPI:
    settings = settings or Settings()
    extractor = Extractor(settings)
    chat_slots = BoundedSemaphore(4)
    app = FastAPI(title="Ontology Engine Extraction Worker", docs_url=None, redoc_url=None, openapi_url=None)

    @app.exception_handler(WorkerError)
    def worker_error(_request: Request, error: WorkerError):
        return JSONResponse({"error": str(error)}, status_code=error.status)

    @app.exception_handler(RequestValidationError)
    def invalid_payload(_request: Request, _error: RequestValidationError):
        return JSONResponse({"error": "Worker 请求格式无效。"}, status_code=422)

    def authorize(request: Request):
        expected = settings.worker_token
        value = request.headers.get("Authorization", "")
        if expected and not hmac.compare_digest(value.encode(), ("Bearer " + expected).encode()):
            raise WorkerError("Worker 请求未通过内部认证。", 401)

    secured = [Depends(authorize)]

    @app.middleware("http")
    async def limit_request(request: Request, call_next):
        length = request.headers.get("content-length")
        if request.method == "POST" and length:
            try:
                if int(length) < 0 or int(length) > MAX_CHAT_SIZE:
                    return JSONResponse({"error": "Worker 请求超过允许的大小。"}, status_code=413)
            except ValueError:
                return JSONResponse({"error": "Worker 请求长度无效。"}, status_code=400)
        return await call_next(request)

    @app.get("/health")
    def health():
        return {"status": "ok", "mock": settings.mock,
                "configured": {"deepseek": bool(settings.llm_key), "feishu": bool(settings.feishu_app and settings.feishu_secret),
                               "config": bool(settings.config_base), "tracking": bool(settings.tracking_base)}}

    @app.post("/chat", dependencies=secured)
    def chat(body: ChatRequest):
        if not chat_slots.acquire(blocking=False):
            raise WorkerError("当前资料处理请求较多，请稍后重试。", 503)
        try:
            return extractor.run_chat(body)
        finally:
            chat_slots.release()

    @app.post("/chat/stream", dependencies=secured)
    def chat_stream(body: ChatRequest):
        if not chat_slots.acquire(blocking=False):
            raise WorkerError("当前资料处理请求较多，请稍后重试。", 503)
        records: Queue[dict] = Queue()

        def run():
            try:
                records.put({"event": "result", "result": extractor.run_chat(body, records.put)})
            except WorkerError as exc:
                records.put({"event": "error", "message": str(exc), "status": exc.status})
            except Exception:
                log.error("Unexpected extraction failure", exc_info=False)
                records.put({"event": "error", "message": "Worker 未能完成资料解析。", "status": 500})
            finally:
                chat_slots.release()

        def lines():
            while True:
                try:
                    record = records.get(timeout=8)
                except Empty:
                    record = {"event": "heartbeat"}
                yield json.dumps(record, ensure_ascii=False) + "\n"
                if record["event"] in {"result", "error"}:
                    break

        Thread(target=run, daemon=True, name="extractor-chat").start()
        return StreamingResponse(lines(), media_type="application/x-ndjson", headers={"X-Accel-Buffering": "no"})

    @app.post("/commit", dependencies=secured)
    def commit(body: CommitRequest):
        return extractor.materialize(body.session_id, body.artifacts or [], body.staging_payload or {})

    @app.post("/query_config_sql", dependencies=secured)
    def query(body: SQLRequest):
        return extractor.database.query(body.sql, body.artifacts or [])

    @app.post("/query_tracking_events", dependencies=secured)
    def tracking(body: TrackingRequest):
        return tracking_definition(settings, body.version, body.event_names or [])

    @app.post("/download_config", dependencies=secured)
    def download_config(body: DownloadRequest):
        return Response(extractor.database.export(body.artifact),
                        media_type="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")

    return app


app = create_app()
