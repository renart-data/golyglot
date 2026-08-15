# golyglot docs

The documentation site uses Astro and Starlight. The `/demo/` page builds the
Go/WASM bridge and runs it in Monaco for tolerant parsing, formatting, and
transpilation.

```sh
corepack pnpm install
corepack pnpm dev
```

The build requires Go 1.25+ to produce `public/golyglot.wasm`.
