# Web console

The browser side of the server: Preact, TypeScript and Vite, built into static files that the server program embeds. Its pages follow the prototype one to one; class names and page codes (A-01 …) are the prototype's so the two can be compared.

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

Before a page's API exists, build against the contract's examples: `VITE_API_MOCK=1 pnpm --dir web dev` answers the console's requests from `src/api/mock.ts` instead of the server. `?mock=admin`, `?mock=owner`, `?mock=member` and `?mock=signed-out` choose who is signed in. Add the endpoints your page needs there, with data shaped like the examples in `api/openapi.yaml`. The mock is never part of a real build.

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
    ├── styles/             the prototype's styles split by component; tokens.gen.css [generated]
    └── theme.ts            light, dark, or follow the system
```

Generated files are committed and checked: `go run ./tools/dev gen` rewrites them, `check` fails when they are stale. `brand.gen.ts`, `tokens.gen.css` and `keys.gen.ts` are made by Go and need no Node; `schema.gen.ts` needs Node and the installed dependencies (openapi-typescript), and is reported as "not run" without them.

## Adding a page

Every page of the prototype already has an id and a path in `src/pages/registry.ts`; most are `notBuilt(...)` stand-ins. The prototype is where a page is designed; build it from there.

1. **Make the directory** `src/pages/<id>/` with an `index.tsx` whose default export is the page. `<id>` is the prototype's page id (the first item of its `NAV` entry: `catalog`, `keys` …). Start with `<PageHeader page="<id>" title={t('nav.<id>')} lead={…} />` so the page code and the breadcrumb match the prototype.
2. **Register it** in `src/pages/registry.ts`: replace `notBuilt('<id>')` with `lazy(() => import('./<id>'))`. A page outside the navigation (sign-in, setup …) goes into `PUBLIC` the same way. Each page becomes its own chunk, loaded when first opened. A sub-path such as `/catalog/<code>` belongs to its page automatically for highlighting; add a route for it next to the page's own.
3. **Styles**: copy the page's section from the prototype's `styles.css` into `src/pages/<id>/<id>.css`, keep the class names, and import it from the page. Shared parts go to `src/styles/` or `src/ui/`. Colours only through the tokens (`var(--ink)` …).
4. **Text**: every string comes from `locales/en/web.json` and `locales/zh-CN/web.json`, added to both in the same change, keys prefixed with the page id (`catalog.filter.search`). Write keys out in full in `t('…')`: `check` reads them to find unused keys, and the type check rejects keys that do not exist. Then `go run ./tools/dev gen`.
5. **Data**: call the API through `api` from `src/api/client.ts`; the types come from the contract, so a contract change the page does not follow fails `tsc`. Show loading, failure and empty states: every list passes its own `Empty` with why it is empty and what to do next. A 403 is the page's to explain — hiding a button is not a permission.
6. **Check**: `go run ./tools/dev check`, then look at the page next to the prototype in both themes and at 390 px wide.

## Rules the content security policy enforces

The server sends a strict policy: scripts and styles only from the server (plus the nonce of the page), no inline event handlers, nothing from other addresses (`connect-src 'self'`). So:

- No `<script>` or `<style>` added by hand, no `onclick="…"` strings, no `dangerouslySetInnerHTML` (lint rejects it). Component props and `style={{ … }}` objects are fine.
- No fonts, scripts, styles, images or requests from any other address. Fonts are in `public/fonts`. Links people click to the outside are fine.
- Every URL goes through `href()` or is relative: the console may be served under a sub-path.
