'use strict';
// xkcd-dashboard — a real npm project (see package.json) built and run inside the
// iceclimber sandbox. It reads a comic JSON (fetched through Popo) and renders a
// blessed-contrib terminal dashboard, exercising blessed-contrib (whose node_modules
// carries .bin symlinks) and its required peer `blessed` — both relayed in as a full
// package.json project. Runs headless (output to a sink, no TTY), then prints the
// computed values the scenario asserts.
const fs = require('fs');
const { Writable } = require('stream');
const blessed = require('blessed');
const contrib = require('blessed-contrib');

const dataPath = process.argv[2];
if (!dataPath) {
  console.error('usage: index.js <comic.json>');
  process.exit(2);
}

const comic = JSON.parse(fs.readFileSync(dataPath, 'utf8'));
const num = comic.num;
const title = comic.title || '';
const titleLen = title.length; // JS string length = UTF-16 code units

// A headless blessed screen: render into a Writable sink (no real terminal).
const sink = new Writable({ write(_c, _e, cb) { cb(); } });
sink.columns = 80;
sink.rows = 24;
const screen = blessed.screen({ smartCSR: false, output: sink, input: process.stdin, terminal: 'xterm' });

// Build a real blessed-contrib dashboard: a grid with a bar chart of the word
// lengths in the comic title — proving both libraries load and drive widgets.
const grid = new contrib.grid({ rows: 12, cols: 12, screen });
const bar = grid.set(0, 0, 12, 12, contrib.bar, {
  label: 'xkcd #' + num + ' — ' + title,
  barWidth: 4,
  maxHeight: 40,
});
const words = title.split(/\s+/).filter(Boolean).slice(0, 6);
bar.setData({ titles: words.map((_w, i) => 'w' + i), data: words.map((w) => w.length) });
screen.render();
screen.destroy();

console.log('DASHBOARD_OK widgets=' + [typeof grid.set, typeof bar.setData].join(','));
console.log('comic #' + num + ': ' + title);
console.log('title length: ' + titleLen);
process.exit(0);
