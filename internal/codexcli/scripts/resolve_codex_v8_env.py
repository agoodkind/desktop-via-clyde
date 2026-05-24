#!/usr/bin/env python3

from __future__ import annotations

import os
from pathlib import Path
import sys


def main() -> int:
    if len(sys.argv) != 3:
        print(
            "usage: resolve_codex_v8_env.py <codex_source_dir> <target>",
            file=sys.stderr,
        )
        return 2

    source_dir = Path(sys.argv[1]).resolve()
    target = sys.argv[2]
    sys.path.insert(0, str(source_dir / "scripts"))

    from codex_package.targets import TARGET_SPECS
    from codex_package.v8 import resolve_codex_v8_cargo_env

    env = resolve_codex_v8_cargo_env(TARGET_SPECS[target], environ=os.environ)
    for key in sorted(env):
        print(f"{key}={env[key]}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
