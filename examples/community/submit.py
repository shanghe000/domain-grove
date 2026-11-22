"""Submit the fictional community graph as a pending local proposal."""
import argparse
import json
from pathlib import Path
from urllib.request import Request, ProxyHandler, build_opener


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--port", type=int, default=8080)
    args = parser.parse_args()
    base = f"http://127.0.0.1:{args.port}"
    client = build_opener(ProxyHandler({}))

    def get(path):
        with client.open(base + path, timeout=5) as response:
            return json.load(response)

    session = get("/api/v1/index/session")
    graph = get("/api/v1/index")
    if session.get("user", {}).get("user_id") != "dev_user" or graph.get("revision") != 0:
        parser.exit(1, "Start the workbench with a fresh local example data directory.\n")
    payload = Path(__file__).with_name("proposal.json").read_bytes()
    request = Request(
        base + "/api/v1/index/modeling/submit", data=payload, method="POST",
        headers={"Content-Type": "application/json", "Origin": base},
    )
    with client.open(request, timeout=5) as response:
        proposal = json.load(response)
    print("Proposal queued:", proposal["id"])


if __name__ == "__main__":
    main()
