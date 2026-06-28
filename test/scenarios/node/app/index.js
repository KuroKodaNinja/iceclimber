'use strict';
// A small Node application built and run inside the iceclimber sandbox. It reads
// a comic JSON (fetched through Popo), computes a few statistics, and renders a
// figlet ASCII banner plus a cli-table3 table — proving the runtime, the two npm
// packages, and execution all composed. Pure CommonJS so it runs via NODE_PATH.
const fs = require('fs');
const figlet = require('figlet');
const Table = require('cli-table3');

const dataPath = process.argv[2];
if (!dataPath) {
  console.error('usage: index.js <comic.json>');
  process.exit(2);
}

const comic = JSON.parse(fs.readFileSync(dataPath, 'utf8'));
const title = comic.title || '';
const alt = (comic.alt || '').trim();
const altWords = alt ? alt.split(/\s+/).length : 0;

console.log(figlet.textSync('xkcd #' + comic.num, { font: 'Standard' }));

const table = new Table({ head: ['Metric', 'Value'] });
table.push(
  ['Number', String(comic.num)],
  ['Title', title],
  ['Title length (chars)', String(title.length)],
  ['Alt words', String(altWords)]
);
console.log(table.toString());
