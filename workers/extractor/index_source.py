"""Short-lived, stdin/stdout adapter. Only schema, locations and business metadata leave this process."""
import hashlib
import json
import os
import sys
from pathlib import PurePosixPath
from urllib.parse import urlsplit

from config_downloader import config_candidate, download, uploaded
from deepseek_agent import DeepSeek, doc_suggestions
from excel_llm_parser import workbook_schemas
from index_anchors import SourceLines, refresh_location, refresh_snapshot
from feishu_to_md import Feishu, feishu_link
from published_config import read_table
from settings import Settings, WorkerError
from tracking_sync import tracking_definition


def digest(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, ensure_ascii=False, default=str).encode()).hexdigest()


def project_settings(space):
    # External configuration/warehouse credentials are project specific. Feishu
    # uses the deployment robot and the explicitly supplied document link.
    from dataclasses import replace
    s = Settings()
    if s.mock or os.getenv("INDEX_SOURCE_PROJECT", "default") == space:
        return s
    return replace(s, config_base="", config_token="", tracking_base="", tracking_token="")


def extract(req):
    s = project_settings(req["space"])
    graph = req["graph"]
    url = req.get("url") or ""
    repository, path = req.get("repository", ""), req.get("path", "")
    model = DeepSeek(s)
    source_lines = None
    suggestions = []
    relations = []
    if url and feishu_link(s, url):
        doc = Feishu(s).read(url)
        title, body = doc["title"], doc["markdown"]
        source_lines = SourceLines(body, ".md")
        repository, path = "feishu/" + urlsplit(url).hostname, "docx/" + doc["doc_id"]
        url = doc["url"]
        anchors = [line.lstrip("# ").strip()[:180] for line in body.splitlines() if line.startswith("#")][:80]
        meta = {"anchors": anchors}
        if s.llm_key or s.mock:
            result = doc_suggestions(s, model, doc, graph)
            for item in result.get("nodes", [])[:8]:
                quote = item.get("quote")
                if not isinstance(quote, str) or len(quote.strip()) < 3 or quote not in body:
                    continue
                kinds = {"Mandatory_Check":"co_check", "Pitfall":"pitfall", "Cross_Validation":"cross_check", "Pruning_Condition":"prune"}
                rules = [{"kind":kinds[a["type"]], "text":a["rule"]} for a in item.get("axioms", [])
                         if a.get("type") in kinds and a.get("quote") and a["quote"] in body and isinstance(a.get("rule"),str)]
                suggestions.append({"key":item.get("id"),"label":item["label"],"description":item.get("description",""),
                                    "axioms":rules,"domain":result.get("domain_id"),"line_start":body[:body.index(quote)].count("\n")+1})
            for e in result.get("edges",[]):
                if e.get("quote") and e["quote"] in body and e.get("predicate")=="impacted_by":
                    relations.append(e)
        shape, content = digest(body), digest(body)
        kind, read = "document", "按章节阅读，必要时通读原文"
    elif req.get("source") == "tracking":
        version = req.get("version") or "latest"
        data = tracking_definition(s, version, [])
        fields = [{"name":event.get("name") or event.get("event_name"),
                   "fields":[{"name":f.get("name"),"type":f.get("type")} for f in event.get("fields",[]) if isinstance(f,dict)]}
                  for event in data["events"] if isinstance(event,dict)]
        if not all(e["name"] for e in fields):
            raise WorkerError("埋点清单含未命名事件。")
        title,repository,path,url="数仓埋点字典","tracking","events",s.tracking_base
        kind,read,meta="event_spec","按版本及事件名过滤",{"events":fields,"source_version":version}
        shape=content=digest(meta)
    elif url and (table_result := read_table(req["space"], url)):
        name,meta,shape,content=table_result
        repository,path=req["space"]+"_config_"+meta["environment"], name
        kind,read,title="config_table","按配置发布清单和主键过滤",name
    elif req.get("file") or (url and config_candidate(s,url)):
        if req.get("file"):
            if not repository or not path:
                raise WorkerError("上传配置表需要填写原仓库和相对路径。")
            name,data,url=uploaded(req["file"])
        else:
            name,data,url=download(s,url)
            repository,path="config/"+urlsplit(url).hostname, urlsplit(url).path.strip("/")+"/"+digest(url)[:16]+"/"+name
        schemas=workbook_schemas(name,data,model.complete if s.llm_key and not s.mock else None)
        meta={"sheets":[{"sheet":t["sheet"],**t["schema"]} for t in schemas]}
        # Only numeric value cells can change silently. Row identities, strings
        # (including semicolon-delimited references), FK values and headers count as structure.
        invariant=[]
        for t in schemas:
            cols=t["schema"]["columns"]
            invariant.append([[v if cols[i]["name"]==t["schema"]["primary_key"] or cols[i]["foreign_key_ref"]
                                or type(v) not in (int,float) else "<numeric>" for i,v in enumerate(row)] for row in t["rows"]])
        shape=digest([meta,invariant]);content=hashlib.sha256(data).hexdigest()
        kind,read,title="config_table","按表名、主键、范围或外键查询",name
    else:
        raise WorkerError("请提供飞书链接、配置下载链接、工作簿或埋点同步请求。")
    identity="artifact."+digest([repository,path])[:24]
    nodes={n["id"]:n for n in graph["nodes"]}
    old=next((n for n in nodes.values() if (n.get("ref") or {}).get("repository")==repository
              and n["ref"].get("path")==path),None)
    if old:
        identity=old["id"]
        if old.get("properties",{}).get("source_shape")==shape:
            return {"status":"matched","id":identity,"kind":kind,"content":content,"shape":shape,
                    "previous":old["properties"].get("source_digest","")}
    label="版本与埋点域" if kind=="event_spec" else "商业化与运营域"
    domain=next((n["id"] for n in nodes.values() if n["kind"]=="domain" and n["label"]==label),None)
    added=[];edges=[]
    if domain is None:
        domain="domain.tracking" if kind=="event_spec" else "domain.live_ops"
        if domain in nodes: raise WorkerError("域编号已被其他用途占用，请先在工作台调整。")
        added.append({"id":domain,"kind":"domain","label":label})
    ref={"repository":repository,"path":path,"read_strategy":read}
    if old and source_lines:
        ref={**old["ref"],**ref}
    if url:ref["url"]=url
    node={**(old or {}),"id":identity,"kind":"artifact","parent_id":(old or {}).get("parent_id",domain),
          "label":(old or {}).get("label",title),"ref":ref,
          "properties":{**(old or {}).get("properties",{}),**meta,"source_digest":content,"source_shape":shape,
                        "cognitive_layer":"S0","artifact_type":kind}}
    added.append(node)
    aliases={}
    def link(src,dst,relation,**extra):
        edge_id="edge."+digest([src,dst,relation,extra.get("source_field"),extra.get("target_field")])[:24]
        edges.append({"id":edge_id,"from":src,"to":dst,"relation":relation,**extra})
    for item in suggestions:
        existing=next((n for n in nodes.values() if n["kind"]=="concept" and n["label"]==item["label"]),None)
        parent=item["domain"] if nodes.get(item["domain"],{}).get("kind")=="domain" else domain
        cid=existing["id"] if existing else "concept."+digest([parent,item["label"]])[:24]
        aliases[item["key"]]=cid
        if not existing:
            added.append({"kind":"concept","id":cid,"label":item["label"],"parent_id":parent,
                          "description":item["description"],"axioms":item["axioms"]})
        anchor=source_lines.heading_at(item["line_start"])
        location={"line_start":item["line_start"]}
        if anchor:
            location=refresh_location({"anchor":anchor},source_lines,set())
        link(cid,identity,"defined_in",**location)
    for e in relations:
        src=e.get("src_id"); dst=aliases.get(e.get("dst_id"),e.get("dst_id"))
        if nodes.get(src,{}).get("kind")=="metric" and (dst in aliases.values() or nodes.get(dst,{}).get("kind")=="concept"):
            link(src,dst,"impacted_by")
    # Reconcile the reviewed target table even when it arrives after its referencing workbook.
    tables=[n for n in [*nodes.values(),node] if n["id"]!=identity or n is node]
    table_owners={sheet["table_name"]:n["id"] for n in tables for sheet in n.get("properties",{}).get("sheets",[]) if isinstance(sheet,dict) and "table_name" in sheet}
    fk_ids=set()
    for n in tables:
        for sheet in n.get("properties",{}).get("sheets",[]):
            for col in sheet.get("columns",[]):
                fk=col.get("foreign_key_ref") or {}
                dst=table_owners.get(fk.get("table"))
                if dst and dst!=n["id"] and identity in (n["id"],dst):
                    link(n["id"],dst,"foreign_key_to",source_field=sheet["table_name"]+"."+col["name"],target_field=fk["table"]+"."+fk["column"])
                    fk_ids.add(edges[-1]["id"])
    removed=[e["id"] for e in graph["edges"] if e["relation"]=="foreign_key_to" and e["from"]==identity
             and e["id"].startswith("edge.") and e["id"] not in fk_ids and old and old.get("properties",{}).get("sheets")]
    changes=[]
    if source_lines:
        fingerprint=hashlib.sha256(body.encode()).hexdigest()
        refreshed,unresolved=refresh_snapshot(graph,node,source_lines,fingerprint)
        if refreshed.get("upsert_nodes"):
            added[added.index(node)]=refreshed["upsert_nodes"][0]
        generated={edge["id"] for edge in edges}
        edges.extend(edge for edge in refreshed.get("upsert_edges",[]) if edge["id"] not in generated)
        changes=refreshed.get("upsert_changes",[])
    return {"title":"资料索引 · "+title,"status":"pending","content":content,"shape":shape,"id":identity,
            "patch":{"schema_version":2,"upsert_nodes":added,"upsert_edges":edges,"remove_edge_ids":removed,"upsert_changes":changes}}


if __name__ == "__main__":
    try:
        result=extract(json.load(sys.stdin))
    except WorkerError as error:
        result={"error":str(error),"code":error.status}
    except Exception:
        result={"error":"来源解析失败，请核对文件格式或模型输出。","code":422}
    print(json.dumps(result,ensure_ascii=False,allow_nan=False))
