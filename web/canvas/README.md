# LoopWorker web canvas

The operator-facing canvas: a React 19 + Vite app that draws the workflow graph,
renders task nodes, and streams state changes over SSE from a running LoopWorker
server.

This is **not** the shipped artifact. `pkg/api/dist` is a committed build output
that the Go server embeds with `//go:embed all:dist`, so a release binary needs
no Node toolchain. If you change anything under `src/`, rebuild and sync `dist`
— see "Rebuilding the embedded web canvas" in the repository `CONTRIBUTING.md`.

## Commands

```bash
npm ci
npm run dev          # vite dev server
npm run lint         # oxlint
npm test             # vitest run
npm run test:watch   # vitest
npm run build        # vite build -> web/canvas/dist
```

Node >= 24. Vitest is configured inside the existing `vite.config.js`; there is
deliberately no second config file.

## Layout

```
src/
  api.js        fetch + SSE client, credential handling, 30s request timeout
  App.jsx       credential gate, graph canvas, task detail drawer
  test-setup.js 40 lines: the React Flow APIs jsdom does not implement
  api.test.js     the HTTP client and the hand-written SSE frame parser
  App.test.jsx    the canvas component tree
```

Tests sit next to the code they cover, not in a separate `tests/` tree.

## What the tests do and do not prove

jsdom renders the component tree; it is not a browser. Covered: the credential
gate and its 401-vs-5xx split, request bounding, lowercase-key state rendering,
plain-text and base64 payload decoding, Escape closing the drawer, tasks created
outside the canvas appearing via `task.created`, lifecycle events updating a
node in place, and the SSE parser under the awkward conditions a real socket
produces — frames split mid-name and mid-JSON, CRLF endings, multi-line `data`
fields, and the opening `stream.opened` frame the server sends. Not covered: node
geometry, pan and zoom, or the binary serving the embedded `dist`.

Every one of these tests was written by putting a known defect back into the
source, watching it fail, then restoring the fix. If you add a test, do the same
before you believe it.