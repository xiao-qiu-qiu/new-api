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

The upstream editor remains a separate frontend package. Host lint and formatter
commands exclude the vendored package and generated assets; use its TypeScript
check and run root oxlint with `--no-ignore` on integration changes. `bun run
canvas:test` runs the upstream Bun tests and integration Vitest suites with the
editor's own `@` alias. The host's selectors reuse `ModelGroupSelector`; the
graph editor, asset library and node tools retain their upstream implementations
because the host has no corresponding canvas editor.

Authentication review (2026-10-14): the `/pg` media routes require the existing
dashboard session access JWT, verify allowed groups on the server and retain
task ownership checks even for administrators. Cookies alone and dashboard PATs
do not authorize media requests. The review used OWASP ASVS 5.0.0 (7.2.1,
7.4.1, 7.4.2, 8.2.1, 8.2.2, 8.3.1, 3.5.1, 3.5.2), the
[Authentication](https://cheatsheetseries.owasp.org/cheatsheets/Authentication_Cheat_Sheet.html),
[Session Management](https://cheatsheetseries.owasp.org/cheatsheets/Session_Management_Cheat_Sheet.html)
and [CSRF Prevention](https://cheatsheetseries.owasp.org/cheatsheets/Cross-Site_Request_Forgery_Prevention_Cheat_Sheet.html)
cheat sheets. This is a review of these routes, not a whole-site ASVS certification.

Focused backend checks passed:

```sh
go test ./router -run '^(TestPlaygroundMediaRelay|TestAccessTokenRouteRulesCoverEveryDashboardRoute|TestGetOpenAIVideoRouteRendersJimengTask)$' -count=1
go test ./middleware ./service -run '^(TestUserAuthNeverFallsBackForRecognizedInvalidInternalJWT|TestUserAuthAppliesAccessTokenRouteRules|TestSessionCookieOriginGuard.*|TestDashboardAccessTokenClassification|TestUserAuthVersionInvalidatesExistingSession|TestRefundTaskQuota_NoToken|TestCalculateAudioQuota.*)$' -count=1
go build ./...
```

These use local upstream fixtures and cover JSON/multipart images and videos,
binary speech, Responses, session rejection, forbidden groups, task ownership,
and user-only settlement without an API token row. No database schema, driver,
SQL or transaction changes were made; no three-database compatibility matrix was
run. Real provider generation and billing still require acceptance on a deployed
instance with the site's configured models. Local project and asset storage is
per user and browser; archives and optional WebDAV provide transfer and backup.
