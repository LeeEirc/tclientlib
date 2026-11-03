#!/usr/bin/env python3
"""Resolve one validated release tag from the workflow's fixed input commit."""

import os
from pathlib import Path
import re
import sys


VERSION_PATTERN = re.compile(
    r"v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)"
    r"(?:-(?P<prerelease>[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?"
)


def resolve_tag(
    event_name: str, ref: str, requested_tag: str, version_file: Path = Path("VERSION")
) -> str:
    """Keep version selection independent from tagging or publishing rights."""
    if event_name == "workflow_dispatch":
        if ref != "refs/heads/master":
            raise ValueError("Run manual releases from master.")
        tag = requested_tag
    elif event_name == "push" and ref == "refs/heads/master":
        lines = version_file.read_text(encoding="utf-8").splitlines()
        if len(lines) != 1:
            raise ValueError("VERSION must contain exactly one version tag.")
        tag = lines[0]
    elif event_name == "push" and ref.startswith("refs/tags/"):
        tag = ref.removeprefix("refs/tags/")
    else:
        raise ValueError("Release requires a master version change, version tag, or manual run.")

    match = VERSION_PATTERN.fullmatch(tag)
    if match is None:
        raise ValueError("Expected vMAJOR.MINOR.PATCH with an optional prerelease suffix.")
    prerelease = match.group("prerelease")
    if prerelease:
        for identifier in prerelease.split("."):
            if identifier.isdigit() and len(identifier) > 1 and identifier[0] == "0":
                raise ValueError("Numeric prerelease identifiers must not have leading zeroes.")
    return tag


def main():
    try:
        tag = resolve_tag(
            os.environ.get("GITHUB_EVENT_NAME", ""),
            os.environ.get("GITHUB_REF", ""),
            os.environ.get("RELEASE_INPUT_TAG", ""),
        )
    except (OSError, UnicodeError, ValueError) as error:
        print(f"Invalid release target: {error}", file=sys.stderr)
        return 1
    print(f"tag={tag}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
