# Infinite Canvas integration

Source: https://github.com/basketikun/infinite-canvas
Revision: `3a6b64b602ad062b29fac66902ed18a7ef5fd9dc`
License: MIT, retained in `LICENSE`.

The upstream editor is built separately and served at `/canvas-app/` inside
the authenticated new-api `/canvas` page. This isolates its Ant Design, router,
and global styles from the host's Base UI components. The host reuses its
`ModelGroupSelector`, authentication client, and theme provider. Upstream canvas
gestures, node editors, asset management, and archive support remain together.

`src/integration/site-context.ts` is the host boundary. Model credentials are
never persisted: generation uses the host's authenticated `/pg` relay. Local
projects and media are scoped to the signed-in user and this browser. Export
projects to transfer or back them up.

Run `bun run build` in `web/` to build both applications. `bun run dev` builds
the embedded editor before starting the host; use `bun run canvas:build` after
editing the editor while the host dev server is running.
