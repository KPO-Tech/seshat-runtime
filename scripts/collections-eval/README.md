# Collections test bench

Measures how well a planner groups the files of an archive into collections, against a known grouping. It is the first step of the plan in
`seshat-ai/docs/produits/knowledge/knowledge-collections-automatiques.md`: the planner it will test does not exist yet; two baselines do.

## What it needs

The documents are local test data that is **not** in this repository (their licences are in the test pack's own README):

- `<testpack>/extractbench/all/{pdfs,truth,md-ro}`: the 252 short documents of ExtractBench, their expected values (whose schema gives the
  type) and the text our reader wrote for each (`md-ro`).
- `<testpack>/cuad/CUAD_v1.zip`: the CUAD contracts. A contract's type is read from its file name; the file is given a neutral name in the
  archive, so the type does not reach the planner.

`pip install numpy scipy scikit-learn`

## Run

```bash
python build_archive.py --testpack <testpack> --out <dir> --p 0.5   # 659 files, 64 types
python evaluate.py --archive <dir> --baseline tree
python evaluate.py --archive <dir> --baseline content --embed-url http://127.0.0.1:18099/v1/embeddings
python evaluate.py --archive <dir> --plan my-plan.json               # {"collections": {"name": ["rel/path", ...]}}
```

`--p` is how often a file sits in the folder of its type (the other files go to generic folders): the tree is informative to that degree.
The content baseline needs an embedding server: `llama-server -m <embedding .gguf> --embedding --port 18099 -c 4096 -ub 2048 -b 2048`.

## Scores

NMI and ARI measure agreement with the true types; NMI forgives a type split in several collections, ARI does not. **to fix** is the number
of files a person would have to move or merge to reach the true grouping: it is the one that says how much work a plan leaves.

## The quick planner (`pkg/collectionplan`)

```bash
go run scripts/collections-eval/plan_folder.go -hash <archive> > plan.json
python evaluate.py --archive <dir> --baseline tree --plan plan.json
```

The planner reads no content (`-hash` reads each file once, on this computer, to find duplicates). Graded on the same 659 files (2026-10-26):

| Folders right | Top-level folders | Planner (default) |
|---|---|---|
| 20 % | 55 collections, 504 to fix (76 %) | 38 collections, 520 (79 %) |
| 50 % | 67 collections, 333 to fix (51 %) | 57 collections, 340 (52 %) |
| 80 % | 67 collections, 142 to fix (22 %) | 64 collections, 146 (22 %) |

It is within 0 to 3 points of the bare folders and gives a plan 3 to 30 % shorter. The default of `MinFiles` comes from this bench: gathering the folders
of up to 4 files (the first value tried) left 58 % to fix at 50 %, against 52 % when only the folders of one file are gathered.

## First results (2026-10-26)

659 files, 64 types (252 forms of 39 types, 407 contracts of 25 types). The content baseline embeds the first 1 600 characters with
`bge-small-en-v1.5` (an English model, 36 MB), cosine distance, average linkage.

| Planner | Collections | NMI | ARI | Purity | To fix |
|---|---|---|---|---|---|
| Top-level folders, p = 0.2 | 55 | 0.331 | 0.019 | 0.259 | 504 (76 %) |
| Top-level folders, p = 0.5 | 67 | 0.595 | 0.195 | 0.531 | 333 (51 %) |
| Top-level folders, p = 0.8 | 67 | 0.850 | 0.700 | 0.804 | 142 (22 %) |
| Content, threshold 0.10 | 508 | 0.786 | 0.206 | 0.997 | 475 (72 %) |
| Content, threshold 0.20 | 354 | 0.804 | 0.317 | 0.920 | 402 (61 %) |
| Content, threshold 0.30 | 75 | 0.735 | 0.275 | 0.543 | 355 (54 %) |

Reading: by NMI the content beats a tree that is right half of the time, but the work left is the same (about half of the files to fix),
because the content makes hundreds of small groups to merge. The content equals a tree that puts about 45 % of the files in the right folder.

Limits: the tree is simulated, with an arbitrary *p*; one small English model, no tuning; the contract types come from file names and
overlap (strategic alliance, cooperation, collaboration); forms are easy to group by template. Not measured: a larger multilingual model,
the whole text, the tree used together with the content, a hierarchy shown to the person.
