"""ml.py — a real PyTorch + pandas program run inside the iceclimber sandbox, from a
conda environment built by `conda.install --file environment.yml` (the air-gapped relay).
It reads the comic JSON fetched through Popo, does a small tensor computation with
PyTorch, tabulates it with pandas, and prints the values the scenario asserts."""
import json
import sys

import numpy as np
import pandas as pd
import torch

comic = json.load(open(sys.argv[1]))
num = comic["num"]
title = comic.get("title", "")

# A small, deterministic PyTorch computation over the title's code points.
codes = [ord(c) for c in title] or [0]
t = torch.tensor(codes, dtype=torch.float32)
code_mean = float(t.mean().item())

# Tabulate with pandas + numpy.
df = pd.DataFrame({"char": list(title), "code": np.array(codes)})

print("MLKIT_OK torch %s pandas %s numpy %s" % (torch.__version__, pd.__version__, np.__version__))
print("comic #%d: %s" % (num, title))
print("title length: %d" % len(title))
print("code mean: %.4f" % code_mean)
print("df rows: %d" % len(df))
