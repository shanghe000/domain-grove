"""Refresh logical source locations without sending document bodies to the index."""
import argparse
import csv
import hashlib
import io
import json
import re
import sys
from collections import defaultdict
from pathlib import Path


class SourceLines:
    def __init__(self, text, suffix):
        self.spans = defaultdict(list)
        self.tabular = suffix in (".csv", ".tsv")
        self.headings = []
        lines = text.splitlines()
        if suffix in (".csv", ".tsv"):
            reader = csv.reader(io.StringIO(text, newline=""), delimiter="," if suffix == ".csv" else "\t")
            start = 1
            for row in reader:
                for cell in set(row):
                    if cell and len(cell) <= 512:
                        self.spans[cell].append((start, reader.line_num))
                start = reader.line_num + 1
        elif suffix in (".yaml", ".yml"):
            stack = []
            for number, line in enumerate(lines, 1):
                key = re.match(r"^( *)([\w.-]+):(?:\s|$)", line)
                if not key:
                    continue
                indent = len(key[1])
                while stack and stack[-1][0] >= indent:
                    _, name, start = stack.pop()
                    self.spans[name].append((start, number - 1))
                stack.append((indent, key[2] + ":", number))
            for _, name, start in stack:
                self.spans[name].append((start, len(lines)))
        elif suffix in (".md", ".markdown"):
            stack = []
            fence = ""
            for number, line in enumerate(lines, 1):
                marker = re.match(r"^ {0,3}(`{3,}|~{3,})", line)
                if marker:
                    if not fence:
                        fence = marker[1]
                    elif marker[1].startswith(fence) and line[marker.end():].strip() == "":
                        fence = ""
                    continue
                match = re.match(r"^ {0,3}(#{1,6})\s+(.+?)\s*$", line)
                if fence or not match:
                    continue
                name = re.sub(r"\s+#+$", "", match[2]).strip()
                depth = len(match[1])
                while stack and stack[-1][0] >= depth:
                    _, old_name, start = stack.pop()
                    self.spans[old_name].append((start, number - 1))
                stack.append((depth, name, number))
                self.headings.append((number, name))
            for _, name, start in stack:
                self.spans[name].append((start, len(lines)))

    def unique_span(self, anchor):
        hits = self.spans.get(anchor, ())
        # Repeated headings or widely separated event occurrences need a
        # more specific business anchor. Never guess from the old line range.
        if len(hits) == 1:
            return hits[0]
        if hits and self.tabular and all(b[0] == a[1] + 1 for a, b in zip(hits, hits[1:])):
            return hits[0][0], hits[-1][1]
        return None

    def heading_at(self, line):
        for start, name in reversed(self.headings):
            span = self.unique_span(name)
            if span and start <= line <= span[1]:
                return name
        return ""


def refresh_location(location, source, unresolved):
    result = dict(location)
    anchor = result.get("anchor", "")
    span = source.unique_span(anchor) if anchor else None
    if span:
        result["line_start"], result["line_end"] = span
    else:
        result.pop("line_start", None)
        result.pop("line_end", None)
        if anchor:
            unresolved.add(anchor)
    return result


def refresh_snapshot(graph, artifact, source, fingerprint):
    ref = artifact["ref"]
    unresolved = set()
    binding = (ref["repository"], ref["path"])
    refreshed = {**artifact, "ref": refresh_location(ref, source, unresolved),
                 "properties": {**artifact.get("properties", {}), "source_sha256": fingerprint}}
    edges = []
    for edge in graph.get("edges", []):
        if edge["to"] == artifact["id"]:
            updated = refresh_location(edge, source, unresolved)
            if updated != edge:
                edges.append(updated)
    changes = []
    for event in graph.get("changes", []):
        location = event.get("ref") or {}
        if (location.get("repository"), location.get("path")) == binding:
            revised = refresh_location(location, source, unresolved)
            if revised != location:
                changes.append({**event, "ref": revised})
    patch = {"schema_version": 2}
    if refreshed != artifact:
        patch["upsert_nodes"] = [refreshed]
    if edges:
        patch["upsert_edges"] = edges
    if changes:
        patch["upsert_changes"] = changes
    return patch, sorted(unresolved)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--graph", required=True, type=Path, help="published get_index_graph JSON")
    parser.add_argument("--node", required=True, help="artifact ID")
    parser.add_argument("--root", required=True, type=Path, help="the explicitly mapped root of this artifact's repository")
    opts = parser.parse_args()
    graph = json.loads(opts.graph.read_text(encoding="utf-8"))
    artifact = next(n for n in graph["nodes"] if n["id"] == opts.node and n["kind"] == "artifact")
    root = opts.root.resolve(strict=True)
    filename = (root / artifact["ref"]["path"]).resolve(strict=True)
    if not filename.is_relative_to(root):
        raise ValueError("source is outside the mapped repository")
    raw = filename.read_bytes()
    fingerprint = hashlib.sha256(raw).hexdigest()
    if artifact.get("properties", {}).get("source_sha256") == fingerprint:
        print(json.dumps({"status":"unchanged"}))
        return
    if filename.suffix.lower() not in {".md", ".markdown", ".csv", ".tsv", ".yml", ".yaml"}:
        parser.error("this source format requires a repository-specific anchor resolver")
    source = SourceLines(raw.decode("utf-8-sig"), filename.suffix.lower())
    patch, unresolved = refresh_snapshot(graph, artifact, source, fingerprint)
    print(json.dumps({"base_revision": graph["revision"], "patch": patch, "unresolved_anchors": unresolved},
                     ensure_ascii=False, allow_nan=False))


if __name__ == "__main__":
    main()
