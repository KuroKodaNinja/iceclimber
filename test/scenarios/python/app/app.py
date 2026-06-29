import json
import sys
from rich.console import Console
from rich.table import Table

# Reads the comic JSON Popo fetched (argv[1]) and renders a computed report with
# rich — a real pip dependency relayed in by Popo.
comic = json.load(open(sys.argv[1]))
num = comic["num"]
title = comic["title"]

table = Table(title="xkcd report")
table.add_column("field")
table.add_column("value")
table.add_row("num", str(num))
table.add_row("title-length", str(len(title)))
Console().print(table)

print(f"[python] xkcd #{num} {title} title-length={len(title)}")
