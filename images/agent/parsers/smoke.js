// Loads each parser from the folder in argv[2] with require() and parses one small input
// with it. Run it in the agent image, as any user and with no network:
//   docker run --rm --network none --user 12345:12345 -v "$PWD/images/agent/parsers:/t:ro" \
//     qory-agent node /t/smoke.js /opt/qory/parsers
'use strict';
const path = require('path');

const dir = path.resolve(process.argv[2] || '');
if (!process.argv[2]) {
  process.stderr.write('Pass the parsers folder: node smoke.js /opt/qory/parsers\n');
  process.exit(2);
}
const load = (name) => require(path.join(dir, 'node_modules', name));
const want = require(path.join(dir, 'package.json')).dependencies;

function expect(cond, what) {
  if (!cond) throw new Error(what);
}

const checks = {
  typescript() {
    const ts = load('typescript');
    const sf = ts.createSourceFile('a.ts', 'const a: number = 1; // c\n', ts.ScriptTarget.Latest, true);
    expect(sf.parseDiagnostics.length === 0 && sf.statements.length === 1, 'does not parse a TypeScript statement');
    const bad = ts.createSourceFile('a.ts', 'const = ;', ts.ScriptTarget.Latest, true);
    expect(bad.parseDiagnostics.length > 0, 'accepts invalid TypeScript');
  },
  async 'libpg-query'() {
    const pg = load('libpg-query');
    await pg.loadModule();
    const tree = await pg.parse('SELECT 1 -- c\n');
    expect(tree.stmts.length === 1, 'does not parse a SELECT');
    let failed = false;
    try {
      await pg.parse('SELEC 1');
    } catch {
      failed = true;
    }
    expect(failed, 'accepts invalid SQL');
  },
  parse5() {
    const { parse } = load('parse5');
    const doc = parse('<p>a<!-- c --></p>');
    const html = doc.childNodes.find((n) => n.nodeName === 'html');
    const p = html.childNodes[1].childNodes[0];
    expect(p.nodeName === 'p' && p.childNodes.some((n) => n.nodeName === '#comment'), 'does not parse a comment');
  },
  postcss() {
    const postcss = load('postcss');
    const root = postcss.parse('a { b: c } /* x */');
    expect(root.nodes.length === 2 && root.nodes[1].type === 'comment', 'does not parse a CSS comment');
  },
  'postcss-scss'() {
    const scss = load('postcss-scss');
    const root = scss.parse('a { b: c } // x\n');
    expect(root.nodes.length === 2 && root.nodes[1].type === 'comment', 'does not parse an SCSS comment');
  },
};

(async () => {
  for (const [name, check] of Object.entries(checks)) {
    const have = require(path.join(dir, 'node_modules', name, 'package.json')).version;
    if (have !== want[name]) {
      process.stderr.write(`Run npm ci in parsers/: ${name} is ${have}, not ${want[name]}\n`);
      process.exit(1);
    }
    try {
      await check();
    } catch (err) {
      process.stderr.write(`Fix ${name} ${have} in ${dir}: it ${err.message}\n`);
      process.exit(1);
    }
  }
  process.stdout.write(`parsers: ${Object.keys(checks).length} packages in ${dir} load and parse\n`);
})();
