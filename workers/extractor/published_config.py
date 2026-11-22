"""Resolve a TSV through a configured project's publication manifest."""
import csv
import hashlib
import io
import json
import os
import re

from settings import WorkerError, fetch, split_url


def read_table(space, link):
    manifest = os.getenv("INDEX_CONFIG_MANIFEST_URL", "")
    if not manifest or space != os.getenv("INDEX_SOURCE_PROJECT", "default"):
        return None
    allowed = split_url(manifest)
    requested = split_url(link)
    if (allowed.scheme, allowed.netloc) != (requested.scheme, requested.netloc):
        return None
    if requested.query or requested.fragment or not re.fullmatch(r"/open/config/download/[0-9]+", requested.path):
        raise WorkerError("请粘贴当前项目发布清单中的单表下载地址。")
    result = json.loads(fetch(manifest, service="配置发布清单", limit=4 << 20))
    matches = [row for row in result["data"] if row["download_url"] == link and row["file_suffix"] == "tsv"]
    if len(matches) != 1:
        raise WorkerError("下载地址与当前环境发布清单不匹配。")
    row = matches[0]
    data = fetch(link, service="配置数据", limit=8 << 20)
    if hashlib.md5(data).hexdigest().lower() != row["md5"].lower():
        raise WorkerError("配置清单与文件指纹不一致，请重试。")
    records = list(csv.reader(io.StringIO(data.decode("utf-8-sig"), newline=""), delimiter="\t"))
    if len(records) < 2 or not records[0] or max(map(len, records)) > 128:
        raise WorkerError("配置表头或列数无效。")
    names, notes = records[:2]
    if len(names) != len(notes) or any(len(r) > len(names) for r in records[2:]):
        raise WorkerError("配置表行与表头不一致。")
    cols = []
    for key, note in zip(names,notes):
        cols.append({"name":key, "comment":note, "confidence":"high" if key else "low"})
    schema={"table_name":row["name"], "sheet":row["name"], "columns":cols,
            "primary_key":"A_INT_id" if "A_INT_id" in names else ""}
    # A TSV carries no typed data cells. Keep reference-like columns structural
    # and avoid classifying numeric IDs as hot-updatable tuning values.
    invariant=[]
    for r in records[2:]:
        if not r: continue
        invariant.append([("<numeric>" if re.fullmatch(r"-?\d+(\.\d+)?",v)
                            and re.search(r"(数量|权重|概率|时长|持续时间)", notes[i])
                            and not re.search(r"(引用|编号|外键|表名|组)",notes[i]) else v)
                          for i,v in enumerate(r)])
    meta={"sheets":[schema],"environment":os.getenv("INDEX_CONFIG_ENV","test")}
    meta_shape=hashlib.sha256(json.dumps([meta,invariant],sort_keys=True,ensure_ascii=False).encode()).hexdigest()
    return row["name"], meta, meta_shape, hashlib.sha256(data).hexdigest()
