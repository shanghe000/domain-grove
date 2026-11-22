from __future__ import annotations

import hashlib
import json
import time
from collections.abc import Callable

from config_downloader import download, uploaded
from deepseek_agent import AXIOMS, CONFIG, FEISHU, TRACKING, DeepSeek, doc_suggestions, source_plan
from duckdb_sandbox import ConfigDatabase
from excel_llm_parser import workbook_schemas
from feishu_to_md import Feishu
from settings import NODE_ID, STAGE_ID, Settings, WorkerError, deadline, now, remaining, write_atomic
from tracking_sync import tracking_definition

MODES = {"feishu_md": "full_text", "duckdb_table": "sql_on_demand", "tracking_api": "api_filter"}
TOOL_LABELS = {FEISHU: "飞书文档", CONFIG: "配置工作簿", TRACKING: "埋点定义"}
EDGE_LABELS = {"depends_on": ("口径依赖", 0.5), "impacted_by": ("受影响于", 1.0),
               "defined_in": ("定义于", 0.5), "configured_by": ("配置于", 0.5), "foreign_key_to": ("外键关联", 0.5)}


def digest(value: str | bytes) -> str:
    return hashlib.sha256(value.encode() if isinstance(value, str) else value).hexdigest()


def node(node_id, label, domain, kind="Artifact", properties=None, axioms=None):
    return {"id": node_id, "node_type": kind, "label": label, "domain_id": domain,
            "status": "active", "properties": properties or {}, "axioms": axioms or []}


def edge(source, target, predicate, metadata=None):
    label, cost = EDGE_LABELS[predicate]
    return {"src_id": source, "dst_id": target, "predicate": predicate, "label_zh": label,
            "cost": cost, "metadata": metadata or {}}


def artifact(node_id, label, kind, ref, *, url="", meta=None, schema_hash=""):
    return {"node_id": node_id, "label": label, "artifact_type": kind, "read_mode": MODES[kind],
            "physical_ref": ref, "source_url": url, "schema_meta": meta or {}, "schema_hash": schema_hash}


def outline(graph: dict, staged: dict) -> dict:
    nodes = {item["id"]: item for item in graph.get("nodes") or [] if isinstance(item, dict) and item.get("id")}
    nodes.update({item["id"]: item for item in staged.get("nodes") or [] if isinstance(item, dict) and item.get("id")})
    for discarded in staged.get("removed_nodes") or []:
        nodes.pop(discarded, None)
    return nodes


def default_domain(nodes: dict, default: str) -> str:
    preferred = nodes.get(default, {})
    if preferred.get("node_type") == "Domain":
        return default
    return next((n["id"] for n in nodes.values() if n.get("node_type") == "Domain"), "")


class Extractor:
    def __init__(self, settings: Settings):
        self.settings = settings
        self.database = ConfigDatabase(settings.database)
        self.feishu = Feishu(settings)
        self.model = DeepSeek(settings)

    def stage_path(self, session_id: str, stage_id: str):
        if not session_id or len(session_id) > 128 or not STAGE_ID.fullmatch(stage_id):
            raise WorkerError("待发布的资料引用无效。")
        return self.settings.data_dir / "staging" / digest(session_id) / (stage_id + ".json")

    def stage(self, session: str, binding: dict, content: dict) -> dict:
        data = json.dumps({"artifact": binding, "content": content}, ensure_ascii=False,
                          sort_keys=True, separators=(",", ":"), allow_nan=False).encode()
        stage_id = digest(data)[:32]
        path = self.stage_path(session, stage_id)
        if not path.exists():
            write_atomic(path, data)
        return {"node_id": binding["node_id"], "type": binding["artifact_type"], "stage_id": stage_id}

    def run_chat(self, request, progress: Callable[[dict], None] | None = None) -> dict:
        token = deadline.set(time.monotonic() + 62)
        events = []

        def emit(tool, status, text):
            event = {"tool": tool, "status": status, "text": text}
            events.append(event)
            if progress:
                progress({"event": "progress", **event})

        try:
            return self.chat(request, emit, events)
        finally:
            deadline.reset(token)

    def chat(self, request, emit, events) -> dict:
        emit("route", "running", "正在识别链接、工作簿与埋点版本。")
        uploads = {}
        for item in request.uploaded_files or []:
            file = uploaded(item)
            if file[0] in uploads:
                raise WorkerError("同一次投喂包含同名文件。请分别上传。")
            uploads[file[0]] = file
        skeleton, draft = request.skeleton or {}, request.draft_patch or {}
        plan = source_plan(self.settings, self.model, request.message, uploads, skeleton, draft)
        emit("route", "done", "已完成输入识别。")
        nodes = outline(skeleton, draft)
        added, edges, bindings = {}, {}, {}
        physical, config_entries = {}, []
        current = {item["node_id"]: item for item in (request.artifacts or []) if item.get("node_id")}
        focus = ""
        for task, ref in plan.sources:
            remaining()
            emit(task, "running", f"正在读取{TOOL_LABELS[task]}。")
            try:
                if task == FEISHU:
                    doc = self.feishu.read(ref)
                    proposal = doc_suggestions(self.settings, self.model, doc, skeleton)
                    focus = self.model_doc(doc, proposal, nodes, added, edges, bindings)
                    physical["Artifact_Doc_" + digest(doc["doc_id"])[:24]] = doc
                elif task == CONFIG:
                    name, body, source_url = uploads[ref] if ref in uploads else download(self.settings, ref)
                    entries = [*current.values(), *(draft.get("artifacts") or [])]
                    known = {
                        meta.get("sheet"): meta for item in entries
                        if item.get("artifact_type") == "duckdb_table"
                        and (meta := item.get("schema_meta") or {}).get("workbook_name", "").casefold() == name.casefold()
                        and meta.get("sheet")
                    }
                    complete = None if self.settings.mock else self.model.complete
                    for data in workbook_schemas(name, body, complete, known):
                        config_entries.append((name, source_url, data))
                elif task == TRACKING:
                    tracked = tracking_definition(self.settings, ref, [])
                    focus = default_domain(nodes, "Domain_BI")
                    node_id = "Artifact_Tracking_" + digest(ref)[:22]
                    label = f"埋点版本 {ref}"
                    added[node_id] = node(node_id, label, focus, properties={"mock": self.settings.mock})
                    known_events = [item.get("name") or item.get("event_name") for item in tracked["events"] if isinstance(item, dict)]
                    bindings[node_id] = artifact(node_id, label, "tracking_api", ref, meta={
                        "version": ref, "event_count": len(known_events), "event_names": known_events[:200],
                        "mock": self.settings.mock,
                    })
                    physical[node_id] = {"version": ref}
                    if "Metric_Retention" in nodes and any(n in {"account_register", "account_login"} for n in known_events):
                        edges[("Metric_Retention", node_id, "depends_on")] = edge("Metric_Retention", node_id, "depends_on")
                nodes.update(added)
            except WorkerError:
                emit(task, "error", f"{TOOL_LABELS[task]}未能读取。")
                raise
            emit(task, "done", f"已读取{TOOL_LABELS[task]}。")

        changed, removed, hot = self.model_tables(config_entries, current, draft, skeleton, nodes,
                                                   added, edges, bindings, physical)
        if changed:
            focus = changed
        references = [self.stage(request.session_id, binding, physical[key]) for key, binding in bindings.items()]
        if hot:
            self.database.write_tables([(value["physical_ref"], value["schema"], value["rows"]) for value in hot],
                                       replace=True)
            emit(CONFIG, "done", "相同结构的配置表已更新数据。")
        patch = {"nodes": list(added.values()), "edges": list(edges.values()), "artifacts": list(bindings.values()),
                 "removed_edges": removed}
        phrases = []
        if bindings:
            phrases.append(f"已把 {len(bindings)} 项来源放到画布草案，可检查节点、关系及证据后确认入库。")
        if plan.operations:
            phrases.append(f"已提出 {len(plan.operations)} 项图谱修改。")
        if hot:
            phrases.append(f"{len(hot)} 张配置表已完成静默数值热更，无需修改本体。")
        if not phrases:
            phrases.append(plan.answer or "可以粘贴飞书文档或配置下载链接、上传 .xlsx，或输入「同步埋点版本 v1.2.0」。")
        return {"message": " ".join(phrases), "draft_patch": patch, "operations": plan.operations,
                "staging_payload": {"sources": references}, "events": events, "focus_domain_id": focus,
                "hot_updates": [{"node_id": value["node_id"], "table_name": value["schema"]["table_name"]} for value in hot]}

    def model_doc(self, doc, proposal, nodes, added, edges, bindings):
        proposed_domain = proposal.get("domain_id", "")
        focus = (proposed_domain if isinstance(proposed_domain, str) and nodes.get(proposed_domain, {}).get("node_type") == "Domain"
                 else default_domain(nodes, "Domain_BI"))
        doc_id = "Artifact_Doc_" + digest(doc["doc_id"])[:24]
        recorded_at = now()
        added[doc_id] = node(doc_id, doc["title"], focus, properties={"mock": self.settings.mock})
        bindings[doc_id] = artifact(doc_id, doc["title"], "feishu_md", f"docs/{doc_id}.md", url=doc["url"],
                                    meta={"title": doc["title"], "mock": self.settings.mock, "fetched_at": recorded_at})
        refs = {key: key for key in nodes}
        labels = {n.get("label"): key for key, n in nodes.items() if n.get("node_type") == "Concept"}

        def evidence(value):
            quote = value.get("quote")
            return quote.strip() if isinstance(quote, str) and 3 <= len(quote.strip()) <= 800 and quote.strip() in doc["markdown"] else ""

        for suggestion in (proposal.get("nodes") or [])[:8] if isinstance(proposal.get("nodes"), list) else []:
            if not isinstance(suggestion, dict) or not evidence(suggestion):
                continue
            key, label = suggestion.get("id"), suggestion.get("label")
            if not isinstance(key, str) or not isinstance(label, str) or not label.strip():
                continue
            label = label.strip()[:120]
            node_id = labels.get(label, "Concept_Doc_" + digest(f"{focus}/{label}")[:24])
            refs[key] = node_id
            if node_id not in nodes:
                axioms = []
                offered = suggestion.get("axioms") or []
                for item in offered[:12] if isinstance(offered, list) else []:
                    if isinstance(item, dict) and item.get("type") in AXIOMS and evidence(item) and isinstance(item.get("rule"), str) and item["rule"].strip():
                        axioms.append({"type": item["type"], "rule": item["rule"].strip()[:600]})
                aliases = suggestion.get("aliases") or []
                description = suggestion.get("description")
                added[node_id] = node(node_id, label, focus, kind="Concept", axioms=axioms, properties={
                    "description": description[:1000] if isinstance(description, str) else "",
                    "aliases": [item.strip()[:100] for item in aliases[:12] if isinstance(item, str) and item.strip()]
                    if isinstance(aliases, list) else [], "evidence_quote": evidence(suggestion), "mock": self.settings.mock,
                })
            labels[label] = node_id
            edges[(node_id, doc_id, "defined_in")] = edge(node_id, doc_id, "defined_in", {"evidence_quote": evidence(suggestion)})
        proposed_edges = proposal.get("edges") or []
        for item in proposed_edges[:16] if isinstance(proposed_edges, list) else []:
            if not isinstance(item, dict) or (predicate := item.get("predicate")) not in {"depends_on", "impacted_by"} or not evidence(item):
                continue
            src_id, dst_id = item.get("src_id"), item.get("dst_id")
            src = refs.get(src_id) if isinstance(src_id, str) else ""
            dst = refs.get(dst_id) if isinstance(dst_id, str) else ""
            if src and dst and src != dst and all((nodes.get(key) or added.get(key) or {}).get("node_type") != "Artifact" for key in (src, dst)):
                edges[(src, dst, predicate)] = edge(src, dst, predicate, {"evidence_quote": evidence(item)})
        return focus

    def model_tables(self, tables, current, draft, skeleton, nodes, added, edges, bindings, physical):
        if not tables:
            return "", [], []
        focus = default_domain(nodes, "Domain_LiveOps")
        updated, hot = set(), []
        table_index = {
            a.get("schema_meta", {}).get("table_name"): a
            for a in [*current.values(), *(draft.get("artifacts") or [])] if a.get("artifact_type") == "duckdb_table"
        }
        incoming = set()
        for file, url, data in tables:
            schema = data["schema"]
            name = schema["table_name"]
            if name in incoming:
                raise WorkerError(f"多个来源被识别为同一配置表 {name}。请分别导入。")
            incoming.add(name)
            previous = table_index.get(name, {})
            node_id = previous.get("node_id") or "Artifact_" + name
            committed = current.get(node_id, {})
            if committed and committed.get("schema_hash") == data["schema_hash"] and previous is committed:
                hot.append({**data, "physical_ref": committed["physical_ref"], "node_id": node_id})
                continue
            title = file if data["sheet"] == "Sheet" else f"{file} · {data['sheet']}"
            domain = nodes.get(node_id, {}).get("domain_id") or focus
            added[node_id] = node(node_id, title, domain, properties={"mock": self.settings.mock})
            bindings[node_id] = artifact(node_id, title, "duckdb_table", name, url=url,
                                        meta={**schema, "sheet": data["sheet"], "workbook_name": file, "mock": self.settings.mock},
                                        schema_hash=data["schema_hash"])
            table_index[name] = bindings[node_id]
            physical[node_id] = data
            updated.add(node_id)
            anchor = ("Concept_RewardGroup" if "reward" in name else "Concept_Activity" if any(key in name for key in ("activity", "boss")) else "")
            if anchor in nodes:
                edges[(anchor, node_id, "configured_by")] = edge(anchor, node_id, "configured_by")

        expected = set()
        for name, binding in table_index.items():
            if not name:
                continue
            for column in (binding.get("schema_meta") or {}).get("columns") or []:
                ref = column.get("foreign_key_ref") or {}
                target = table_index.get(ref.get("table"))
                if not target or target["node_id"] == binding["node_id"]:
                    continue
                target_columns = (target.get("schema_meta") or {}).get("columns") or []
                if not any(c.get("name") == ref.get("column") for c in target_columns):
                    continue
                src_id, dst_id = binding["node_id"], target["node_id"]
                if src_id not in updated and dst_id not in updated:
                    continue
                key = (src_id, dst_id, "foreign_key_to")
                expected.add(key)
                join = f"{name}.{column['name']} = {ref['table']}.{ref['column']}"
                relation = edges.setdefault(key, edge(*key, {"join_clause": ""}))
                existing = relation["metadata"]["join_clause"]
                relation["metadata"]["join_clause"] = f"{existing} AND {join}" if existing else join
        removed = {}
        for old in [*(skeleton.get("edges") or []), *(draft.get("edges") or [])]:
            key = old.get("src_id"), old.get("dst_id"), old.get("predicate")
            if key[2] == "foreign_key_to" and (key[0] in updated or key[1] in updated) and key not in expected:
                if all(any(a.get("node_id") == endpoint for a in table_index.values()) for endpoint in key[:2]):
                    removed[key] = {"src_id": key[0], "dst_id": key[1], "predicate": key[2]}
        return focus if updated else "", list(removed.values()), hot

    def materialize(self, session: str, artifacts: list[dict], staging_payload: dict) -> dict:
        sources = {(item.get("node_id"), item.get("type")): item.get("stage_id") for item in staging_payload.get("sources") or []}
        published, updates = [], []
        for accepted in artifacts:
            slot = (accepted.get("node_id"), accepted.get("artifact_type"))
            stage = sources.get(slot)
            if not stage:
                published.append(accepted)
                continue
            node_id, kind = slot
            if not isinstance(node_id, str) or not NODE_ID.fullmatch(node_id) or kind not in MODES:
                raise WorkerError("待发布资料的节点或类型无效。")
            path = self.stage_path(session, stage)
            try:
                stage_body = path.read_bytes()
                if digest(stage_body)[:32] != stage:
                    raise ValueError("checksum")
                payload = json.loads(stage_body)
                binding, content = payload["artifact"], payload["content"]
            except (OSError, ValueError, KeyError, TypeError) as exc:
                raise WorkerError("待发布原件不可用，请重新导入并生成草案。", 409) from exc
            if binding["node_id"] != node_id or binding["artifact_type"] != kind:
                raise WorkerError("待发布资料与草稿不一致。", 409)

            if kind == "feishu_md":
                document = self.markdown(binding, content)
                revision = digest(document)[:32]
                out = self.settings.data_dir / "docs" / "revisions" / node_id / (revision + ".md")
                if not out.exists():
                    write_atomic(out, document)
                binding["schema_meta"]["content_revision"] = revision
            elif kind == "duckdb_table":
                schema = content["schema"]
                metadata = binding["schema_meta"]
                approved = (accepted.get("schema_meta") or {}).get("columns") or []
                annotations = {col.get("name"): col for col in approved if isinstance(col, dict)}
                for col in metadata["columns"]:
                    update = annotations.get(col["name"], {})
                    if isinstance(update.get("comment"), str):
                        col["comment"] = update["comment"][:1000]
                    confidence = update.get("confidence")
                    if confidence in {"high", "low"}:
                        col["confidence"] = confidence
                binding["physical_ref"] = "cfg_rev_" + digest(session + "." + stage)[:32]
                updates.append((binding["physical_ref"], schema, content["rows"]))
            binding["updated_at"] = now()
            published.append(binding)
        if updates:
            self.database.write_tables(updates)
        return {"artifacts": published}

    @staticmethod
    def markdown(binding, doc) -> bytes:
        frontmatter = {"node_id": binding["node_id"], "title": doc["title"], "feishu_url": doc["url"],
                       "updated_at": binding["schema_meta"]["fetched_at"]}
        header = "\n".join(key + ": " + json.dumps(value, ensure_ascii=False) for key, value in frontmatter.items())
        clean = "\n".join(line for line in doc["markdown"].splitlines()).strip()
        return f"---\n{header}\n---\n\n# {doc['title']}\n\n{clean}\n".encode()
