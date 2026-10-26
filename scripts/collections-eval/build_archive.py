"""Builds a messy archive with a known grouping, to test how well a planner finds the collections of an archive.

The documents come from local test data that is not part of the repository (see README.md). Each document has a true label (its type).
The archive puts a document in a folder named after its type with the probability --p, and in a generic folder otherwise, so that the
folder tree is informative to a chosen degree. Writes <out>/archive/ (the files), <out>/labels.json and <out>/meta.json.

Usage: python build_archive.py --testpack <rag-testpack> --out <dir> [--p 0.5] [--seed 1] [--only forms|contracts]
"""
import argparse
import hashlib
import json
import os
import random
import re
import shutil
import zipfile
from pathlib import Path

GENERIC = ["Divers", "A trier", "Scans 2024", "Nouveau dossier", "Archive ancienne", "Documents", "Reception", "Anciens"]
YEARS = ["2019", "2020", "2021", "2022", "2023", "2024"]


def form_documents(testpack: Path):
    """The short documents of the ExtractBench split: a PDF, its text as our reader wrote it, and its type (the schema of the values)."""
    base = testpack / "extractbench" / "all"
    out = []
    for truth in sorted((base / "truth").glob("*.json")):
        stem = truth.stem
        pdf = base / "pdfs" / f"{stem}.pdf"
        text = base / "md-ro" / f"{stem}.pdf.md"
        if not pdf.exists():
            continue
        schema = json.loads(truth.read_text(encoding="utf-8"))["data_schema"]
        kind = hashlib.md5(str(tuple(sorted(schema.get("properties", {})))).encode()).hexdigest()[:6]
        name = (schema.get("title") or "form")[:40]
        out.append(dict(src=pdf, text=text if text.exists() else None, label=f"form:{kind}", folder=re.sub(r"[^A-Za-z0-9]+", "_", name).strip("_") or f"form_{kind}"))
    return out


def contract_type(file_name: str):
    """The type of a CUAD contract, read from the end of its file name ("... SERVICES AGREEMENT" is a service contract)."""
    base = file_name.rsplit("/", 1)[-1][:-4].upper()
    m = re.search(r"((?:[A-Z&]+[ \-]){0,2}[A-Z&]+)[ _\.]*AGREEMENT[^A-Z]*$", base) or re.search(r"([A-Z&]+(?: [A-Z&]+)?) (?:AGREEMENT|CONTRACT)", base)
    if not m:
        return None
    t = re.sub(r"^(?:EX[-\.]?\d+[\.\d]*[- ]*)", "", m.group(1).strip())
    return "SERVICE" if t == "SERVICES" else t


def contract_documents(testpack: Path, workdir: Path, minimum: int = 6):
    zf = testpack / "cuad" / "CUAD_v1.zip"
    with zipfile.ZipFile(zf) as z:
        names = [n for n in z.namelist() if n.lower().endswith(".txt") and "full_contract_txt" in n.lower()]
        labelled = {n: contract_type(n) for n in names}
        counts = {}
        for t in labelled.values():
            if t:
                counts[t] = counts.get(t, 0) + 1
        out = []
        for n, t in labelled.items():
            if t and counts[t] >= minimum:
                dst = workdir / (hashlib.md5(n.encode()).hexdigest()[:10] + ".txt")
                if not dst.exists():
                    dst.write_bytes(z.read(n))
                # The file keeps a neutral name: its type is read from the original name, which must not reach the planner.
                out.append(dict(src=dst, text=dst, label=f"contract:{t}", folder=t.title().replace(" ", "_") + "_agreements"))
    return out


def link_or_copy(src: Path, dst: Path):
    dst.parent.mkdir(parents=True, exist_ok=True)
    try:
        os.link(src, dst)
    except OSError:
        shutil.copy2(src, dst)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--testpack", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--p", type=float, default=0.5, help="probability that a document is in the folder of its type")
    ap.add_argument("--seed", type=int, default=1)
    ap.add_argument("--only", choices=["forms", "contracts"])
    a = ap.parse_args()
    testpack, out = Path(a.testpack), Path(a.out)
    shutil.rmtree(out / "archive", ignore_errors=True)
    (out / "work").mkdir(parents=True, exist_ok=True)

    docs = []
    if a.only != "contracts":
        docs += form_documents(testpack)
    if a.only != "forms":
        docs += contract_documents(testpack, out / "work")
    rng = random.Random(a.seed)
    labels, used = {}, set()
    for d in docs:
        right = rng.random() < a.p
        folder = d["folder"] if right else rng.choice(GENERIC)
        sub = rng.choice(YEARS) if rng.random() < 0.5 else ""
        name = d["src"].name
        rel = Path(folder) / sub / name
        while rel.as_posix() in used:
            rel = rel.with_name(f"{rng.randrange(10**6)}_{name}")
        used.add(rel.as_posix())
        link_or_copy(d["src"], out / "archive" / rel)
        labels[rel.as_posix()] = dict(label=d["label"], text=str(d["text"]) if d["text"] else None)
    (out / "labels.json").write_text(json.dumps(labels, indent=1), encoding="utf-8")
    types = sorted({v["label"] for v in labels.values()})
    (out / "meta.json").write_text(json.dumps(dict(p=a.p, seed=a.seed, files=len(labels), types=len(types)), indent=1), encoding="utf-8")
    print(f"{len(labels)} files, {len(types)} types, p={a.p}, written to {out}")


if __name__ == "__main__":
    main()
