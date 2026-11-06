"""Deterministically balance discovered example pairs using recorded exchange times."""

import argparse
import json
import re
import statistics
import sys
from pathlib import Path


def plan(names, count, seconds):
    if count < 1 or not names or len(set(names)) != len(names):
        raise ValueError("need unique test names and a positive shard count")
    known = [seconds[name] for name in names if name in seconds]
    default = statistics.median(known) if known else 1.0
    weights = {name: seconds.get(name, default) for name in names}
    shards = [[] for _ in range(min(count, len(names)))]
    totals = [0.0] * len(shards)
    for name in sorted(names, key=lambda name: (-weights[name], name)):
        lightest = min(range(len(shards)), key=lambda index: (totals[index], index))
        shards[lightest].append(name)
        totals[lightest] += weights[name]
    return [sorted(shard) for shard in shards]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("count", type=int)
    args = parser.parse_args()
    seconds = json.loads(Path(__file__).with_name("example_pair_seconds.json").read_text())["seconds"]
    names = sys.stdin.read().splitlines()
    for shard in plan(names, args.count, seconds):
        print("^(" + "|".join(re.escape(name) for name in shard) + ")$")


if __name__ == "__main__":
    main()
