import json
import sys

import numpy as np
import pandas as pd

# Reads the comic JSON Popo fetched (argv[1]) and processes it with pandas + numpy —
# real C-extension pip dependencies (with musllinux wheels) relayed in by Popo. This is
# the pip-relay counterpart to the conda torch+pandas scenario.
comic = json.load(open(sys.argv[1]))
num = comic["num"]
title = comic.get("title", "")

codes = np.array([ord(c) for c in title] or [0])
df = pd.DataFrame({"char": list(title), "code": codes})

print("PANDAS_OK pandas %s numpy %s" % (pd.__version__, np.__version__))
print("xkcd #%d: %s" % (num, title))
print("title length: %d" % len(title))
print("code mean: %.4f" % float(df["code"].mean()))
print("df rows: %d" % len(df))
