"""Extract the tagged release's public notes from CHANGELOG.md."""

import argparse
from pathlib import Path


def release_notes(changelog: str, tag: str) -> str:
    lines = changelog.splitlines(keepends=True)
    heading = f"## [{tag}]"
    matches = [index for index, line in enumerate(lines)
               if line.rstrip().partition(" - ")[0] == heading]
    if len(matches) != 1:
        raise ValueError(f"Expected one changelog section for {tag}, found {len(matches)}")
    start = matches[0] + 1
    end = next((index for index in range(start, len(lines))
                if lines[index].startswith("## ")), len(lines))
    body = "".join(lines[start:end]).strip()
    if not any(line.strip() and not line.lstrip().startswith("#")
               for line in body.splitlines()):
        raise ValueError(f"Changelog section for {tag} has no release notes")
    return body + "\n"


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("tag")
    parser.add_argument("--changelog", type=Path, default=Path("CHANGELOG.md"))
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    try:
        body = release_notes(args.changelog.read_text(encoding="utf-8"), args.tag)
    except ValueError as error:
        parser.error(str(error))
    args.output.write_text(body, encoding="utf-8")


if __name__ == "__main__":
    main()
