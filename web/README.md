# Web console

The browser side of the server: Preact, TypeScript and Vite, built into static files that the server program embeds. Every page has a page code (A-01 …) shown in its header, so a screenshot or a bug report names the page unambiguously.

Only people who change `web/`, release builds and browser tests need Node (22.12 or later) and pnpm. Everyone else builds and tests the Go code as usual: without the built console the server embeds a one-page stub that says how to build it.

## Commands

All from the repository root.

| What                                                          | Command                                                                             |
| ------------------------------------------------------------- | ----------------------------------------------------------------------------------- |
| Install the dependencies (needs the network; a separate step) | `go run ./tools/dev web-deps`                                                       |
| Build into `web/dist`                                         | `go run ./tools/dev web`                                                            |
| Build the server with the console in it                       | `go build -tags webdist ./cmd/...`                                                  |
| Format, lint, type check, unit tests, test build              | part of `go run ./tools/dev check` (the "web console" step; "not run" without Node) |
| Regenerate the generated files                                | `go run ./tools/dev gen`                                                            |

Inside `web/` the usual `pnpm exec prettier --check .`, `pnpm exec eslint .`, `pnpm exec tsc --noEmit`, `pnpm exec vitest run` and `pnpm build` work too.

## Working on a page

Run the development server and point the Go server at it; the console then reloads as you edit, and it is served exactly as in production (same base path handling, same headers, same content security policy apart from inline styles, which the development server needs):

```sh
pnpm --dir web dev                                          # http://127.0.0.1:5173
RGD_WEB_DEV_PROXY=http://127.0.0.1:5173 go run ./cmd/rgd-server   # plus the usual RGD_BASE_URL …
```

Open the server's address, not 5173. The proxy works when the server is at the root of its address (no path in `RGD_BASE_URL`).

Before a page's API exists, build against the contract's examples: `VITE_API_MOCK=1 pnpm --dir web dev` answers the console's requests from `src/api/mock.ts` instead of the server. `?mock=admin`, `?mock=owner`, `?mock=member` and `?mock=signed-out` choose who is signed in. Add the endpoints your page needs there, with data shaped like the examples in `api/openapi.yaml`. `src/api/mock-auth.ts` (sign-in, setup, device confirmation, users) adds `?mock=pending` (an account waiting for approval), `?mock=setup` and `?mock=setup-admin` (a server whose first-run wizard is open), `?mockfail=403`, `?mockfail=500` or `?mockfail=network` (every call but the console's start fails that way; `?mockfail=off` stops it) and `?mockempty=1` (empty lists). The mock is never part of a real build.

## Layout

```
web/
├── index.html              the page; the server fills in __BASE_HREF__, __LANG__, __DEFAULT_LOCALE__, __CSP_NONCE__
├── tokens/tokens.json      design tokens, the source of src/styles/tokens.gen.css
├── public/                 fonts (SIL OFL 1.1, with their licences) and brand files, copied as they are
├── stub/index.html         what the server shows when it was built without the console
├── embed_dist.go           -tags webdist: embed dist/
├── embed_stub.go           default: embed stub/
└── src/
    ├── main.tsx  app.tsx   start: language, /api/v1/meta, /api/v1/me, routing
    ├── brand.gen.ts        names, from brand.json                     [generated]
    ├── api/                client.ts (openapi-fetch), schema.gen.ts   [generated], mock.ts
    ├── i18n/               t(), the language, number and date formats; keys.gen.ts [generated]
    ├── router/paths.ts     href(): every link and route goes through it (the console may live under a sub-path)
    ├── shell/              sidebar, navigation, account menu, theme switch, feedback button
    ├── pages/              registry.ts (navigation and routes) and one directory per page
    ├── ui/                 shared parts: icons, logo, page header, empty state, kind shapes
    ├── styles/             the shared styles, split by component; tokens.gen.css [generated]
    └── theme.ts            light, dark, or follow the system
```

Generated files are committed and checked: `go run ./tools/dev gen` rewrites them, `check` fails when they are stale. `brand.gen.ts`, `tokens.gen.css` and `keys.gen.ts` are made by Go and need no Node; `schema.gen.ts` needs Node and the installed dependencies (openapi-typescript), and is reported as "not run" without them.

## Adding a page

Every page has an id and a path in `src/pages/registry.ts`. A page that is planned but not built yet is a `notBuilt(...)` stand-in there. Look at an existing page of the same kind (a list with a drawer, a settings form …) and follow it.

1. **Make the directory** `src/pages/<id>/` with an `index.tsx` whose default export is the page. `<id>` is the page's id in the registry (`catalog`, `keys` …). Start with `<PageHeader page="<id>" title={t('nav.<id>')} lead={…} />` so the page code and the breadcrumb are right.
2. **Register it** in `src/pages/registry.ts`: replace `notBuilt('<id>')` with `lazy(() => import('./<id>'))`. A page outside the navigation (sign-in, setup …) goes into `PUBLIC` the same way. Each page becomes its own chunk, loaded when first opened. A sub-path such as `/catalog/<code>` belongs to its page automatically for highlighting; add a route for it next to the page's own.
3. **Styles**: put the page's own styles in `src/pages/<id>/<id>.css` and import it from the page; reuse the classes in `src/styles/` before adding new ones. Shared parts go to `src/styles/` or `src/ui/`. Colours only through the tokens (`var(--ink)` …).
4. **Text**: every string comes from `locales/en/web.json` and `locales/zh-CN/web.json`, added to both in the same change, keys prefixed with the page id (`catalog.filter.search`). Write keys out in full in `t('…')`: `check` reads them to find unused keys, and the type check rejects keys that do not exist. Then `go run ./tools/dev gen`.
5. **Data**: call the API through `api` from `src/api/client.ts`; the types come from the contract, so a contract change the page does not follow fails `tsc`. Show loading, failure and empty states: every list passes its own `Empty` with why it is empty and what to do next. A 403 is the page's to explain — hiding a button is not a permission.
6. **Check**: `go run ./tools/dev check`, then look at the page in both themes, at 390 px wide and in both languages.

## Rules the content security policy enforces

The server sends a strict policy: scripts and styles only from the server (plus the nonce of the page), no inline event handlers, nothing from other addresses (`connect-src 'self'`). So:

- No `<script>` or `<style>` added by hand, no `onclick="…"` strings, no `dangerouslySetInnerHTML` (lint rejects it). Component props and `style={{ … }}` objects are fine.
- No fonts, scripts, styles, images or requests from any other address. Fonts are in `public/fonts`. Links people click to the outside are fine.
- Every URL goes through `href()` or is relative: the console may be served under a sub-path.
