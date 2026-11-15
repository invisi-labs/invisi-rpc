#!/usr/bin/env python3
"""Print the exact tagged version section from the source changelog."""
import re
import sys
from pathlib import Path

if len(sys.argv) != 2 or not re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+", sys.argv[1]):
    raise SystemExit("Expected a stable version tag, e.g. v1.3.0")
version = sys.argv[1][1:]
text = (Path(__file__).resolve().parents[1] / "CHANGELOG.md").read_text()
sections = re.split(r"^## ", text, flags=re.MULTILINE)[1:]
matches = [part for part in sections if part.splitlines()[0].strip() == version]
if len(matches) != 1 or not matches[0].partition("\n")[2].strip():
    raise SystemExit("Missing or duplicate nonempty changelog section for " + version)
print("## " + matches[0].strip())
