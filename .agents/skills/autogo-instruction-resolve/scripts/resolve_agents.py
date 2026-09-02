#!/usr/bin/env python3
"""Resolve project instruction chains for the autogo-instruction-resolve Skill."""
from __future__ import annotations

import argparse
import json
from pathlib import Path

INSTRUCTIONS_FILE = "AGENTS.md"


def chain(root: Path, target: Path) -> list[Path]:
    target = target.resolve()
    # New files may not exist yet. A suffix is a conservative hint that the
    # supplied path is a file; callers can pass the parent directory when a
    # dotted directory name is intended.
    if target.is_file() or (not target.exists() and target.suffix):
        target = target.parent
    try:
        relative = target.relative_to(root)
    except ValueError as exc:
        raise ValueError(f"path is outside project root: {target}") from exc

    candidates = [root]
    current = root
    for part in relative.parts:
        current = current / part
        candidates.append(current)
    return [directory / INSTRUCTIONS_FILE for directory in candidates if (directory / INSTRUCTIONS_FILE).is_file()]


def main() -> int:
    parser = argparse.ArgumentParser(description="Resolve effective project instruction files")
    parser.add_argument("paths", nargs="+", help="Affected files or directories")
    parser.add_argument("--root", default=".", help="Project root")
    parser.add_argument("--json", action="store_true")
    args = parser.parse_args()

    root = Path(args.root).expanduser().resolve()
    if not (root / INSTRUCTIONS_FILE).exists():
        raise SystemExit(f"project root instruction file not found: {root / INSTRUCTIONS_FILE}")

    result: dict[str, list[str]] = {}
    unique: list[str] = []
    for raw in args.paths:
        target = (root / raw).resolve() if not Path(raw).is_absolute() else Path(raw).resolve()
        resolved = [str(p.relative_to(root)) for p in chain(root, target)]
        result[raw] = resolved
        for item in resolved:
            if item not in unique:
                unique.append(item)

    if args.json:
        print(json.dumps({"paths": result, "effective_instructions": unique}, ensure_ascii=False, indent=2))
    else:
        for item in unique:
            print(item)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
