#!/usr/bin/env python3
"""Builds the conversation file used by the live compaction benchmark from real agent trajectories.

The trajectories are from the Hugging Face dataset nebius/SWE-agent-trajectories. The output is a JSON list of tasks,
each a list of {"role": "user"|"assistant", "text": ...}; the benchmark joins them into one long conversation.

    python scripts/live-conv-from-hf.py conv.json
    SESHAT_LIVE_CONV=conv.json SESHAT_LIVE_PROVIDER=zai SESHAT_LIVE_MODEL=glm-4.5 ZHIPUAI_API_KEY=... \
      go test -tags live ./internal/runtime/memory/ -run TestLiveAutoCompactionAtTheThreshold -v

The data is read as JSON and never executed.
"""
import json
import sys
import urllib.request

URL = ("https://datasets-server.huggingface.co/rows?dataset=nebius%2FSWE-agent-trajectories"
       "&config=default&split=train&offset=0&length=10")
PICK = [0, 1, 2, 6]  # four long trajectories, about 440,000 characters together


def main(out: str) -> None:
    with urllib.request.urlopen(URL, timeout=60) as resp:
        rows = json.load(resp)["rows"]
    tasks = []
    for i in PICK:
        messages = []
        for m in rows[i]["row"]["trajectory"]:
            text = (m.get("text") or "").strip()
            if m["role"] == "system" or not text:
                continue
            messages.append({"role": "assistant" if m["role"] == "ai" else "user", "text": text})
        tasks.append(messages)
    with open(out, "w", encoding="utf-8") as f:
        json.dump(tasks, f)
    print(f"wrote {out}: {[len(t) for t in tasks]} messages")


if __name__ == "__main__":
    main(sys.argv[1] if len(sys.argv) > 1 else "conv.json")
