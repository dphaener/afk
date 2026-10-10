#!/usr/bin/env bash
set -euo pipefail
mkdir -p src test data docs
cat > README.md <<'EOT'
# tally

A small Node CLI that counts lines and words in text files.

    node src/cli.js count <file>

Every run appends one entry to `data/history.json`, so people can look back
at earlier counts with `node src/cli.js history`.
EOT
cat > package.json <<'EOT'
{
  "name": "tally",
  "version": "0.3.0",
  "private": true,
  "type": "module",
  "scripts": {
    "test": "vitest run"
  },
  "devDependencies": {
    "vitest": "^2.0.0"
  }
}
EOT
cat > vitest.config.js <<'EOT'
import { defineConfig } from 'vitest/config';

export default defineConfig({
  test: { include: ['test/**/*.test.js'] },
});
EOT
cat > CONTRIBUTING.md <<'EOT'
# Contributing

- Source lives in `src/`, one command per file, named after the command
  (`src/count.js` for `count`).
- Tests live in `test/`, one `<command>.test.js` per command.
- Plain ES modules, no TypeScript, no new dependencies without a discussion.
- Every user-visible change gets a line in `docs/changelog.md`.
EOT
cat > docs/changelog.md <<'EOT'
# Changelog

## 0.3.0
- `history` command lists earlier counts.

## 0.2.0
- `count` reports words as well as lines.
EOT
cat > src/count.js <<'EOT'
import { readFileSync } from 'node:fs';

export function count(path) {
  const text = readFileSync(path, 'utf8');
  const lines = text === '' ? 0 : text.split('\n').length;
  const words = text.split(/\s+/).filter(Boolean).length;
  return { lines, words };
}
EOT
cat > src/history.js <<'EOT'
import { readFileSync, writeFileSync, existsSync } from 'node:fs';

const FILE = 'data/history.json';

export function load() {
  return existsSync(FILE) ? JSON.parse(readFileSync(FILE, 'utf8')) : [];
}

export function record(entry) {
  const all = load();
  all.push({ ...entry, at: new Date().toISOString() });
  writeFileSync(FILE, JSON.stringify(all, null, 2));
}
EOT
cat > src/cli.js <<'EOT'
import { count } from './count.js';
import { load, record } from './history.js';

const [command, arg] = process.argv.slice(2);

if (command === 'count') {
  const result = count(arg);
  record({ file: arg, ...result });
  console.log(`${result.lines} lines, ${result.words} words`);
} else if (command === 'history') {
  for (const e of load()) console.log(`${e.at} ${e.file} ${e.lines}/${e.words}`);
} else {
  console.error('usage: tally count <file> | history');
  process.exit(1);
}
EOT
cat > test/count.test.js <<'EOT'
import { describe, it, expect } from 'vitest';
import { writeFileSync } from 'node:fs';
import { count } from '../src/count.js';

describe('count', () => {
  it('counts lines and words', () => {
    writeFileSync('test/tmp.txt', 'one two\nthree\n');
    expect(count('test/tmp.txt')).toEqual({ lines: 3, words: 3 });
  });
});
EOT
cat > data/history.json <<'EOT'
[
  { "file": "notes.txt", "lines": 12, "words": 80, "at": "2025-01-14T09:30:00.000Z" },
  { "file": "draft.md", "lines": 40, "words": 410, "at": "2025-02-02T16:05:00.000Z" }
]
EOT
printf 'node_modules/\n' > .gitignore
git init -q
git add -A
git -c user.email=seed@example.com -c user.name=seed commit -qm seed
