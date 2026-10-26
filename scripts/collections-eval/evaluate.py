"""Grades a grouping of an archive into collections against the true types of its files.

A plan is a JSON file {"collections": {"<name>": ["<relative path>", ...], ...}}, written by whatever planner is tested. Two baselines are
built in, so that any planner can be compared with them:
  --baseline tree      the top-level folders of the archive are the collections (what a folder-tree planner does)
  --baseline content   the text of each file is embedded by an OpenAI-compatible server and the vectors are clustered (hierarchical,
                       cosine, average linkage); --threshold gives the distance at which clusters are cut (several values may be given)

Scores (the labels are the true types in labels.json, written by build_archive.py):
  NMI, ARI    agreement with the true types, from 0 (none) to 1 (identical); NMI forgives a type split in several collections, ARI does not
  purity      share of the files that are in a collection whose majority type is their own
  to fix      files that a person would have to move or merge to reach the true grouping: every collection is matched to at most one type
              (best matching), and the files of a collection that is not matched, or that are not of the type it was matched to, count
Usage: python evaluate.py --archive <dir with labels.json> (--plan plan.json | --baseline tree | --baseline content --embed-url URL)
Needs numpy, scipy and scikit-learn.
"""
import argparse
import json
import urllib.error
import urllib.request
from pathlib import Path

import numpy as np
from scipy.optimize import linear_sum_assignment
from sklearn.metrics import adjusted_rand_score, normalized_mutual_info_score


def load(archive: Path):
    labels = json.loads((archive / "labels.json").read_text(encoding="utf-8"))
    paths = sorted(labels)
    types = sorted({labels[p]["label"] for p in paths})
    y = np.array([types.index(labels[p]["label"]) for p in paths])
    return paths, labels, y


def scores(y, pred):
    pred = np.asarray(pred)
    table = np.zeros((len(set(pred)), y.max() + 1), dtype=int)
    index = {c: i for i, c in enumerate(sorted(set(pred)))}
    for c, t in zip(pred, y):
        table[index[c], t] += 1
    rows, cols = linear_sum_assignment(-table)
    matched = int(table[rows, cols].sum())
    return dict(
        collections=len(index),
        nmi=normalized_mutual_info_score(y, pred),
        ari=adjusted_rand_score(y, pred),
        purity=float(table.max(axis=1).sum() / len(y)),
        to_fix=len(y) - matched,
        to_fix_share=(len(y) - matched) / len(y),
    )


def plan_from_file(plan_path: Path, paths):
    plan = json.loads(plan_path.read_text(encoding="utf-8"))["collections"]
    owner = {p: name for name, files in plan.items() for p in files}
    return [owner.get(p, "<unplaced>") for p in paths]


def plan_from_tree(paths):
    return [p.split("/")[0] for p in paths]


def embed(url: str, text: str):
    n = 1600
    while n >= 150:
        body = json.dumps({"input": [text[:n]], "model": "x"}).encode()
        try:
            req = urllib.request.Request(url, data=body, headers={"Content-Type": "application/json"})
            return json.load(urllib.request.urlopen(req, timeout=300))["data"][0]["embedding"]
        except urllib.error.HTTPError:
            n //= 2  # too long for the model: try a shorter start
    return None


def plans_from_content(paths, labels, url, thresholds):
    from sklearn.cluster import AgglomerativeClustering

    vectors = []
    for p in paths:
        src = labels[p].get("text")
        text = " ".join(Path(src).read_text(encoding="utf-8", errors="replace").split()) if src and Path(src).exists() else ""
        vectors.append(embed(url, text or Path(p).stem))
    dim = len(next(v for v in vectors if v))
    e = np.array([v if v else [0.0] * dim for v in vectors], dtype="float32")
    norm = np.linalg.norm(e, axis=1, keepdims=True)
    e /= np.where(norm == 0, 1, norm)
    for th in thresholds:
        yield th, AgglomerativeClustering(n_clusters=None, distance_threshold=th, metric="cosine", linkage="average").fit_predict(e)


def show(name, s):
    print(f"{name:42} {s['collections']:5d}   {s['nmi']:.3f}  {s['ari']:.3f}  {s['purity']:.3f}   {s['to_fix']:5d} ({s['to_fix_share']:.0%})")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--archive", required=True, help="the directory written by build_archive.py")
    ap.add_argument("--plan")
    ap.add_argument("--baseline", choices=["tree", "content"])
    ap.add_argument("--embed-url", default="http://127.0.0.1:18099/v1/embeddings")
    ap.add_argument("--threshold", type=float, action="append")
    ap.add_argument("--json", help="also write the scores to this file")
    a = ap.parse_args()
    paths, labels, y = load(Path(a.archive))
    meta = json.loads((Path(a.archive) / "meta.json").read_text(encoding="utf-8"))
    print(f"{len(paths)} files, {y.max() + 1} true types, folder tree informativeness p={meta['p']}")
    print(f"{'planner':42} {'colls':>5}   {'NMI':>5}  {'ARI':>5}  {'purity':>6}   {'to fix':>12}")
    result = {}
    if a.plan:
        result["plan"] = scores(y, plan_from_file(Path(a.plan), paths))
        show("plan " + a.plan, result["plan"])
    if a.baseline == "tree":
        result["tree"] = scores(y, plan_from_tree(paths))
        show("baseline: top-level folders", result["tree"])
    if a.baseline == "content":
        for th, pred in plans_from_content(paths, labels, a.embed_url, a.threshold or [0.10, 0.15, 0.20, 0.25, 0.30]):
            result[f"content@{th}"] = scores(y, pred)
            show(f"baseline: content, threshold {th:.2f}", result[f"content@{th}"])
    if a.json:
        Path(a.json).write_text(json.dumps(result, indent=1), encoding="utf-8")


if __name__ == "__main__":
    main()
