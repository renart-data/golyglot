import { execFileSync } from 'node:child_process';
import { copyFileSync, mkdirSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const docsRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const repoRoot = resolve(docsRoot, '..');
const publicRoot = join(docsRoot, 'public');
const wasmPath = join(publicRoot, 'golyglot.wasm');
const wasmExecPath = join(publicRoot, 'wasm_exec.js');
const goRoot = execFileSync('go', ['env', 'GOROOT'], { encoding: 'utf8' }).trim();

mkdirSync(publicRoot, { recursive: true });
copyFileSync(join(goRoot, 'lib', 'wasm', 'wasm_exec.js'), wasmExecPath);
execFileSync('go', ['build', '-o', wasmPath, './cmd/golyglot-wasm'], {
	cwd: repoRoot,
	env: { ...process.env, GOOS: 'js', GOARCH: 'wasm' },
	stdio: 'inherit',
});
