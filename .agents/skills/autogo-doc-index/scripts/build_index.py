#!/usr/bin/env python3
"""Build docs INDEX.md files for the autogo-doc-index Skill.

This helper is intentionally mechanical. It does not decide whether a workspace
or document should exist; the native Agent and its Skills make that decision.
"""
from __future__ import annotations

import argparse
import os
import re
import tempfile
from pathlib import Path

BEGIN = "<!-- AGENT-HARNESS:BEGIN INDEX -->"
END = "<!-- AGENT-HARNESS:END INDEX -->"


def atomic_write(path: Path, text: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, name = tempfile.mkstemp(prefix=f".{path.name}.", dir=str(path.parent))
    try:
        with os.fdopen(fd, "w", encoding="utf-8", newline="\n") as handle:
            handle.write(text)
            handle.flush()
        os.replace(name, path)
    finally:
        try:
            os.unlink(name)
        except FileNotFoundError:
            pass


def first_heading(text: str) -> str:
    match = re.search(r"(?m)^#\s+(.+?)\s*$", text)
    return match.group(1).strip() if match else ""


def escape_cell(value: str) -> str:
    return value.replace("|", "\\|").replace("\n", " ").strip()


def managed_replace(existing: str, body: str) -> str:
    block = f"{BEGIN}\n{body.rstrip()}\n{END}"
    start = existing.find(BEGIN)
    finish = existing.find(END)
    if start >= 0 and finish >= start:
        finish += len(END)
        return (existing[:start] + block + existing[finish:]).rstrip() + "\n"
    if existing.strip():
        return existing.rstrip() + "\n\n" + block + "\n"
    return "# Workspace Index\n\n" + block + "\n"


def build_workspace(workspace: Path, create: bool = False) -> tuple[int, Path]:
    if not workspace.is_dir():
        raise ValueError(f"workspace does not exist: {workspace}")
    index = workspace / "INDEX.md"
    if not index.exists() and not create:
        raise ValueError(f"INDEX.md does not exist: {index}; create the workspace from the template first")

    rows: list[tuple[str, str, str]] = []
    for path in sorted(workspace.glob("*.md")):
        if path.name in {"README.md", "INDEX.md"}:
            continue
        text = path.read_text(encoding="utf-8")
        doc_id = path.stem
        title = first_heading(text) or path.stem
        rows.append((doc_id, title, path.name))

    lines = [
        "| ID | 标题 | 文件 |",
        "|---|---|---|",
    ]
    for doc_id, title, filename in rows:
        lines.append(
            "| " + " | ".join(
                [
                    escape_cell(doc_id),
                    escape_cell(title),
                    f"[{escape_cell(filename)}]({filename})",
                ]
            ) + " |"
        )

    existing = index.read_text(encoding="utf-8") if index.exists() else ""
    atomic_write(index, managed_replace(existing, "\n".join(lines)))
    return len(rows), index


def discover_workspaces(root: Path) -> list[Path]:
    docs = root / "docs"
    if not docs.exists():
        return []
    return sorted({p.parent for p in docs.rglob("INDEX.md") if p.is_file()})


def main() -> int:
    parser = argparse.ArgumentParser(description="Rebuild Agent-managed docs indexes")
    parser.add_argument("workspaces", nargs="*", help="Workspace paths relative to the project root")
    parser.add_argument("--root", default=".", help="Project root")
    parser.add_argument("--all", action="store_true", help="Build every existing docs workspace containing INDEX.md")
    parser.add_argument("--create", action="store_true", help="Create a minimal INDEX.md if missing")
    args = parser.parse_args()

    root = Path(args.root).expanduser().resolve()
    targets = discover_workspaces(root) if args.all else [(root / p).resolve() for p in args.workspaces]
    if not targets:
        parser.error("provide at least one workspace or use --all")

    for target in targets:
        try:
            target.relative_to(root)
        except ValueError as exc:
            raise SystemExit(f"workspace must be inside project root: {target}") from exc
        count, index = build_workspace(target, create=args.create)
        print(f"updated {index.relative_to(root)} ({count} documents)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
