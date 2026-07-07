import { spawnSync } from 'node:child_process';
import { createRequire } from 'node:module';
import {
  copyFileSync,
  existsSync,
  mkdirSync,
  readdirSync,
  readFileSync,
  rmSync,
  statSync,
  writeFileSync,
} from 'node:fs';
import { dirname, extname, join, relative, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const require = createRequire(import.meta.url);
const svelteVersion = '3.59.2';
const scriptDir = dirname(fileURLToPath(import.meta.url));
const frontendDir = resolve(scriptDir, '..');
const repoRoot = resolve(frontendDir, '..');
const srcDir = join(frontendDir, 'src');
const depsDir = join(frontendDir, '.deps');
const webDir = join(repoRoot, 'backend', 'web');
const assetsDir = join(webDir, 'assets');
const runtimeDir = join(assetsDir, 'internal');

function ensureDir(path) {
  mkdirSync(path, { recursive: true });
}

function walk(dir) {
  const out = [];
  for (const entry of readdirSync(dir)) {
    const full = join(dir, entry);
    if (statSync(full).isDirectory()) {
      out.push(...walk(full));
    } else {
      out.push(full);
    }
  }
  return out;
}

async function download(url, destination) {
  const response = await fetch(url);
  if (!response.ok) {
    throw new Error(`download failed ${response.status}: ${url}`);
  }
  const buffer = Buffer.from(await response.arrayBuffer());
  writeFileSync(destination, buffer);
}

function extractTarball(tarball, destination) {
  const result = spawnSync('tar', ['-xzf', tarball, '-C', destination], {
    stdio: 'inherit',
  });
  if (result.status !== 0) {
    throw new Error('tar extraction failed');
  }
}

async function ensureSveltePackage() {
  try {
    return require.resolve('svelte/compiler');
  } catch {
    // Fallback for environments with Node but without npm installed.
  }

  const packageDir = join(depsDir, 'svelte', 'package');
  const compilerPath = join(packageDir, 'compiler.js');
  if (existsSync(compilerPath)) return compilerPath;

  ensureDir(join(depsDir, 'svelte'));
  const tarball = join(depsDir, `svelte-${svelteVersion}.tgz`);
  await download(`https://registry.npmjs.org/svelte/-/svelte-${svelteVersion}.tgz`, tarball);
  extractTarball(tarball, join(depsDir, 'svelte'));
  if (!existsSync(compilerPath)) {
    throw new Error(`svelte compiler not found at ${compilerPath}`);
  }
  return compilerPath;
}

function rewriteSvelteImports(code) {
  return code
    .replace(/from\s+(['"])([^'"]+)\.svelte\1/g, 'from $1$2.js$1')
    .replace(/import\(\s*(['"])([^'"]+)\.svelte\1\s*\)/g, 'import($1$2.js$1)');
}

function writeRuntimeFiles(packageDir) {
  ensureDir(runtimeDir);
  const svelteIndex = readFileSync(join(packageDir, 'index.mjs'), 'utf8')
    .replace('./internal/index.mjs', './internal/index.js');
  const internalIndex = readFileSync(join(packageDir, 'internal', 'index.mjs'), 'utf8');
  writeFileSync(join(assetsDir, 'svelte.js'), svelteIndex, 'utf8');
  writeFileSync(join(runtimeDir, 'index.js'), internalIndex, 'utf8');
}

function writeIndex() {
  const html = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>ProxyForge</title>
  <link rel="stylesheet" href="/style.css">
  <script type="importmap">
    {
      "imports": {
        "svelte": "/assets/svelte.js",
        "svelte/internal": "/assets/internal/index.js"
      }
    }
  </script>
</head>
<body>
  <div id="app"></div>
  <script type="module" src="/assets/main.js"></script>
</body>
</html>
`;
  writeFileSync(join(webDir, 'index.html'), html, 'utf8');
}

function compileSourceFiles(compiler) {
  for (const sourcePath of walk(srcDir)) {
    const rel = relative(srcDir, sourcePath);
    const ext = extname(sourcePath);
    if (rel === 'style.css') {
      copyFileSync(sourcePath, join(webDir, 'style.css'));
      continue;
    }
    if (ext === '.svelte') {
      const source = readFileSync(sourcePath, 'utf8');
      const compiled = compiler.compile(source, {
        filename: sourcePath,
        format: 'esm',
        css: false,
        dev: false,
      });
      const outPath = join(assetsDir, rel.replace(/\.svelte$/, '.js'));
      ensureDir(dirname(outPath));
      writeFileSync(outPath, rewriteSvelteImports(compiled.js.code), 'utf8');
      continue;
    }
    if (ext === '.js') {
      const outPath = join(assetsDir, rel);
      ensureDir(dirname(outPath));
      writeFileSync(outPath, rewriteSvelteImports(readFileSync(sourcePath, 'utf8')), 'utf8');
    }
  }
}

async function main() {
  const compilerPath = await ensureSveltePackage();
  const compiler = require(compilerPath);
  const packageDir = dirname(compilerPath);

  rmSync(webDir, { recursive: true, force: true });
  ensureDir(assetsDir);
  writeIndex();
  writeRuntimeFiles(packageDir);
  compileSourceFiles(compiler);

  console.log(`Svelte built to ${webDir}`);
}

main().catch((error) => {
  console.error(error);
  process.exit(1);
});
