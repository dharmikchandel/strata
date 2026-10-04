#!/usr/bin/env python3
"""Derive log message templates, with their real frequencies, from the BGL dataset.

One-off developer tool. Its output (internal/gen/templates_bgl.json) is committed so
the generator does not need the dataset. Run it again only to regenerate that file:

    python3 bench/demo/derive_templates.py ~/strata-datasets/BGL.log internal/gen/templates_bgl.json

A line is "label epoch date node timestamp node RAS component level message...".
Digits in the message are replaced by placeholders, so every distinct path or id does
not become its own template:  {node} for node names, {hex} for 0x... values, {n} for numbers.
"""
import collections, json, re, sys

src, out = sys.argv[1], sys.argv[2]
TOP = 500
node_re = re.compile(r'\bR\d\d-M\d-N[0-9A-F]-C:J\d\d-U\d\d\b|\bR\d\d-M\d-N[0-9A-F]-I:J\d\d-U\d\d\b|\bR\d\d-M\d-NC-I:J\d\d-U\d\d\b|\bR\d\d-M\d-L\d-U\d\d-[A-Z]\b|\bR\d\d-M\d\b')
hex_re = re.compile(r'0x[0-9a-fA-F]+')
num_re = re.compile(r'\d+')

counts = collections.Counter()
total = 0
with open(src, errors='replace') as f:
    for line in f:
        p = line.rstrip('\n').split(' ', 9)
        if len(p) < 9:
            continue
        label, component, level = p[0], p[7], p[8]
        msg = p[9] if len(p) > 9 else ''
        msg = node_re.sub('{node}', msg)
        msg = hex_re.sub('{hex}', msg)
        msg = num_re.sub('{n}', msg)
        counts[(label, component, level, msg)] += 1
        total += 1

top = counts.most_common(TOP)
covered = sum(c for _, c in top)
items = [{'weight': c, 'label': k[0], 'component': k[1], 'level': k[2], 'text': k[3]} for k, c in top]
json.dump({'source': 'BGL (LogHub), Oliner and Stearley, DSN 2007', 'lines': total, 'distinct_templates': len(counts),
           'covered_lines': covered, 'templates': items}, open(out, 'w'), indent=0)
print(f'{total} lines, {len(counts)} distinct templates; top {TOP} cover {100*covered/total:.2f}%')
