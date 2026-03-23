"""
Auto-discover markdown files from the repo and expose them as virtual MkDocs pages.

README.md files in any directory are included as the page for that directory.
DISASTER_RECOVERY.md and other root-level .md files are included as top-level pages.
PLAN.md files are excluded (internal planning docs, not committed).
.pages files are copied so mkdocs-awesome-pages-plugin picks up nav labels.

Run by mkdocs-gen-files during `mkdocs serve` / `mkdocs build`.
"""

from pathlib import Path

import mkdocs_gen_files

ROOT = Path(".")
EXCLUDE_DIRS = {".git", ".github", "docs", "site", "security-policies"}


def is_excluded(path: Path) -> bool:
    return any(part in EXCLUDE_DIRS for part in path.parts)


# Copy markdown files into the virtual docs directory
for md_path in sorted(ROOT.rglob("*.md")):
    if is_excluded(md_path):
        continue
    if md_path.name == "PLAN.md":
        continue
    with mkdocs_gen_files.open(str(md_path), "w") as f:
        f.write(md_path.read_text())

# Copy .pages files so mkdocs-awesome-pages-plugin picks up nav labels and ordering
for pages_path in sorted(ROOT.rglob(".pages")):
    if is_excluded(pages_path):
        continue
    with mkdocs_gen_files.open(str(pages_path), "w") as f:
        f.write(pages_path.read_text())
