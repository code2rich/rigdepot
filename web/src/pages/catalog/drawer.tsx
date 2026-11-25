// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// A resource's details (the prototype's resDrawer): what it is for, a sentence to try it, how it is
// delivered, its content as it lands on the computer, what it needs from me, and where it is installed on
// my computers. Opened from the catalog (/catalog/<code>) and from the home page. Usage figures and the
// quality grade come with the insights module (M2).

import { useState } from 'preact/hooks'
import { api, unwrap, type Schemas } from '../../api/client'
import { BRAND } from '../../brand.gen'
import { fmtDateTime } from '../../i18n/format'
import { t, type MessageKey } from '../../i18n'
import { href } from '../../router/paths'
import { openFeedback } from '../../shell/feedback-dialog'
import { Icon } from '../../ui/icons'
import { LoadError, Loading, useLoad } from '../../ui/load'
import { Drawer } from '../../ui/overlay'
import {
  KindIcon,
  KindTag,
  PolicyBadge,
  StageBadges,
  codeLabel,
  kindName,
  policyWhat,
  scopeName,
  slashName,
  toolName,
} from '../../ui/resource'
import { AiTip, CodeBox } from '../../ui/say'

const HINT: Record<string, MessageKey> = {
  skill: 'catalog.detail.hint.skill',
  agent: 'catalog.detail.hint.agent',
  workflow: 'catalog.detail.hint.workflow',
}

/** The biggest file shown inline; bigger ones are listed but not fetched. */
const MAX_PREVIEW = 256 * 1024

export function ResourceDrawer(props: { code: string; fresh: boolean; onClose: () => void }) {
  const res = useLoad(
    async () => unwrap(await api.GET('/api/v1/catalog/{code}', { params: { path: { code: props.code } } })),
    [props.code],
  )
  const r = res.data
  const label = r?.name ?? props.code
  return (
    <Drawer
      label={label}
      onClose={props.onClose}
      head={
        <div class="row">
          <KindIcon kind={r?.kind ?? ''} />
          <div style={{ minWidth: 0 }}>
            <h2>{label}</h2>
            {r && (
              <p class="mono">
                {t('catalog.detail.sub', { kind: kindName(r.kind), code: codeLabel(r), version: r.version })}
              </p>
            )}
          </div>
        </div>
      }
    >
      {res.state === 'loading' && <Loading />}
      {res.state === 'failed' && <LoadError error={res.error} retry={res.reload} />}
      {r && <Detail r={r} fresh={props.fresh} onClose={props.onClose} />}
    </Drawer>
  )
}

function Detail(props: { r: Schemas['CatalogResource']; fresh: boolean; onClose: () => void }) {
  const { r } = props
  const def = r.definition as Record<string, unknown>
  return (
    <>
      <div style={{ display: 'grid', gap: '12px' }}>
        {r.summary && (
          <p class="dim" style={{ fontSize: '14px' }}>
            {r.summary}
          </p>
        )}
        {r.try && (
          <AiTip
            say={r.try}
            label={t('catalog.detail.try')}
            note={props.fresh ? t('catalog.detail.try.fresh') : t('catalog.detail.try.note')}
          />
        )}
      </div>
      <dl class="kv lined">
        <dt>{t('catalog.detail.kind')}</dt>
        <dd>
          <KindTag kind={r.kind} />
        </dd>
        <dt>{t('catalog.detail.stages')}</dt>
        <dd>
          <div class="row" style={{ gap: '4px' }}>
            <StageBadges stages={r.stages} />
          </div>
        </dd>
        <dt>{t('catalog.detail.scope')}</dt>
        <dd>{scopeName(r.scope)}</dd>
        <dt>{t('catalog.detail.policy')}</dt>
        <dd>
          <PolicyBadge policy={r.policy} /> <span class="faint t12">{policyWhat(r.policy)}</span>
        </dd>
        {r.kind === 'workflow' && (
          <>
            <dt>{t('catalog.detail.how')}</dt>
            <dd>
              {t('catalog.detail.how.workflow')} <span class="mono">{slashName(r.code)}</span>
            </dd>
          </>
        )}
        {r.kind === 'agent' && (
          <>
            <dt>{t('catalog.detail.how')}</dt>
            <dd>{t('catalog.detail.how.agent', { name: r.name })}</dd>
          </>
        )}
        {r.kind === 'mcp' && r.delivery && (
          <>
            <dt>{t('catalog.detail.delivery')}</dt>
            <dd>
              {r.delivery === 'on_demand' ? (
                <>
                  <span class="badge brand">{t('catalog.delivery.on_demand')}</span>{' '}
                  <span class="faint t12">{t('catalog.delivery.on_demand.what')}</span>
                </>
              ) : (
                <>
                  <span class="badge">{t('catalog.delivery.native')}</span>{' '}
                  <span class="faint t12">{t('catalog.delivery.native.what')}</span>
                </>
              )}
            </dd>
          </>
        )}
        <dt>{t('catalog.detail.version')}</dt>
        <dd>
          <span class="mono">v{r.version}</span>{' '}
          <span class="faint t12">
            {t('catalog.detail.published', { at: fmtDateTime(r.published_at, { dateStyle: 'medium' }) })}
          </span>
        </dd>
        {r.maintainers && r.maintainers.length > 0 && (
          <>
            <dt>{t('catalog.detail.maintainers')}</dt>
            <dd>{r.maintainers.map((m) => m.display_name).join(t('ui.list_sep'))}</dd>
          </>
        )}
      </dl>
      {r.kind === 'workflow' && <Steps def={def} />}
      {r.kind === 'mcp' && <McpTools def={def} />}
      {r.kind === 'mcp' && r.delivery !== 'on_demand' && (
        <div>
          <div class="sect-t">{t('catalog.detail.config')}</div>
          <CodeBox
            text={JSON.stringify(
              {
                mcpServers: { [BRAND.prefix + r.code]: { command: BRAND.cli, args: ['mcp', 'run', r.code] } },
              },
              null,
              2,
            )}
          />
          <p class="hint">{t('catalog.detail.hint.mcp')}</p>
        </div>
      )}
      {r.kind === 'mcp' && r.delivery === 'on_demand' && (
        <p class="hint" style={{ margin: 0 }}>
          {t('catalog.detail.hint.on_demand')}
        </p>
      )}
      {r.files.length > 0 && <Files r={r} />}
      {r.kind === 'agent' && r.references.length > 0 && (
        <div>
          <div class="sect-t">
            {t('catalog.detail.binds')} <span class="n">{t('catalog.detail.binds.note')}</span>
          </div>
          <RefList refs={r.references} badge={t('catalog.detail.binds.with')} />
        </div>
      )}
      <Needs r={r} onClose={props.onClose} />
      <Installs r={r} fresh={props.fresh} onClose={props.onClose} />
      {r.referenced_by.length > 0 && (
        <div>
          <div class="sect-t">{t('catalog.detail.used_by')}</div>
          <RefList refs={r.referenced_by} />
        </div>
      )}
      <div>
        <div class="sect-t">{t('catalog.detail.good')}</div>
        <div class="row">
          <button
            type="button"
            class="btn"
            onClick={() => {
              props.onClose()
              openFeedback(r.code)
            }}
          >
            <Icon name="down" />
            {t('catalog.detail.bad')}
          </button>
        </div>
      </div>
    </>
  )
}

function RefList(props: { refs: Schemas['ResourceRef'][]; badge?: string }) {
  return (
    <div class="list">
      {props.refs.map((x) => (
        <a
          key={x.code}
          class="li"
          href={href(`/catalog/${x.code}`)}
          style={{ padding: '6px 0', color: 'inherit', textDecoration: 'none' }}
        >
          <KindIcon kind={x.kind} />
          <div class="grow">
            <div class="name">{x.name}</div>
            <div class="desc">
              {kindName(x.kind)} · <span class="mono">{x.code}</span>
            </div>
          </div>
          {props.badge && <span class="badge ok">{props.badge}</span>}
        </a>
      ))}
    </div>
  )
}

type Step = {
  use?: unknown
  checks?: { text?: unknown; by?: unknown; run?: unknown }[]
  forbid?: unknown[]
  then?: unknown
}

const BY: Record<string, MessageKey> = {
  ai: 'catalog.step.by.ai',
  command: 'catalog.step.by.command',
  human: 'catalog.step.by.human',
}

/** Steps is a workflow's steps, from its definition (workflows/<code>.yaml). */
function Steps(props: { def: Record<string, unknown> }) {
  const steps = Array.isArray(props.def.steps) ? (props.def.steps as Step[]) : []
  if (steps.length === 0) return null
  return (
    <div>
      <div class="sect-t">
        {t('catalog.detail.steps')}{' '}
        <span class="n">{t('catalog.detail.steps.note', { count: steps.length })}</span>
      </div>
      <div class="wf">
        {steps.map((s, i) => (
          <div key={i}>
            <div class="wf-step">
              <span class="no">{i + 1}</span>
              <div class="grow">
                <div class="row" style={{ gap: '8px' }}>
                  <b class="mono">{String(s.use ?? '')}</b>
                  {(s.forbid ?? []).map((f, j) => (
                    <span key={j} class="badge bad">
                      <Icon name="ban" />
                      {t('catalog.step.forbid', { what: String(f) })}
                    </span>
                  ))}
                </div>
                {s.checks && s.checks.length > 0 && (
                  <ul>
                    {s.checks.map((c, j) => {
                      const by = BY[String(c.by)]
                      return (
                        <li key={j}>
                          {String(c.text ?? '')}{' '}
                          {by && <span class={c.by === 'human' ? 'badge warn' : 'badge'}>{t(by)}</span>}
                          {typeof c.run === 'string' && <span class="mono faint">{c.run}</span>}
                        </li>
                      )
                    })}
                  </ul>
                )}
              </div>
            </div>
            {i < steps.length - 1 && (
              <div class={s.then === 'confirm' ? 'wf-then stop' : 'wf-then'}>
                <span>
                  {s.then === 'confirm' ? t('catalog.step.then.confirm') : t('catalog.step.then.continue')}
                </span>
              </div>
            )}
          </div>
        ))}
      </div>
      <p class="hint">{t('catalog.detail.steps.hint')}</p>
    </div>
  )
}

/** McpTools lists an MCP server's tools, from its definition (mcp/<code>.yaml). */
function McpTools(props: { def: Record<string, unknown> }) {
  const tools = Array.isArray(props.def.tools)
    ? (props.def.tools as { name?: unknown; summary?: unknown; writes?: unknown }[])
    : []
  if (tools.length === 0) return null
  return (
    <div>
      <div class="sect-t">
        {t('catalog.detail.tools')}{' '}
        <span class="n">{t('catalog.detail.tools.count', { count: tools.length })}</span>
      </div>
      <div class="list">
        {tools.map((x, i) => (
          <div key={i} class="li" style={{ padding: '8px 0' }}>
            <div class="grow">
              <div class="mono" style={{ color: 'var(--ink)' }}>
                {String(x.name ?? '')}
              </div>
              {typeof x.summary === 'string' && <div class="desc">{x.summary}</div>}
            </div>
            {x.writes === true ? (
              <span class="badge warn">{t('catalog.detail.tools.writes')}</span>
            ) : (
              <span class="badge">{t('catalog.detail.tools.reads')}</span>
            )}
          </div>
        ))}
      </div>
    </div>
  )
}

/** Files shows the resource's files as they land on the computer, one tab per file. */
function Files(props: { r: Schemas['CatalogResource'] }) {
  const { r } = props
  const [current, setCurrent] = useState(0)
  const file = r.files[Math.min(current, r.files.length - 1)]
  const content = useLoad(async () => {
    if (!file || file.size > MAX_PREVIEW) return null
    const res = await api.GET('/api/v1/catalog/{code}/file', {
      params: { path: { code: r.code }, query: { path: file.path, version: r.version } },
      parseAs: 'text',
    })
    return unwrap(res) as unknown as string
  }, [r.code, r.version, file?.path])
  const hint = HINT[r.kind]
  return (
    <div>
      <div class="sect-t">
        {t('catalog.detail.content')} <span class="n">v{r.version}</span>
      </div>
      {r.files.length > 1 && (
        <div
          class="seg"
          role="tablist"
          style={{ marginBottom: '8px' }}
          aria-label={t('catalog.detail.content')}
        >
          {r.files.map((f, i) => (
            <button
              key={f.path}
              type="button"
              role="tab"
              aria-selected={i === current}
              class={i === current ? 'on' : undefined}
              onClick={() => setCurrent(i)}
            >
              <span class="mono">{f.path}</span>
            </button>
          ))}
        </div>
      )}
      {content.state === 'loading' && <Loading />}
      {content.state === 'failed' && <LoadError error={content.error} retry={content.reload} />}
      {content.state === 'ready' &&
        (content.data === null ? (
          <p class="dim t13">{t('catalog.detail.too_big')}</p>
        ) : (
          <CodeBox text={content.data} label={file?.path} />
        ))}
      {hint && <p class="hint">{t(hint)}</p>}
    </div>
  )
}

/** Needs lists what the resource needs from me: personal secrets, settings for each computer. */
function Needs(props: { r: Schemas['CatalogResource']; onClose: () => void }) {
  const { r } = props
  const secrets = r.secrets ?? []
  const local = r.local_config ?? []
  if (secrets.length === 0 && local.length === 0) return null
  return (
    <div>
      <div class="sect-t">{t('catalog.detail.needs')}</div>
      <div class="list">
        {secrets.map((s) => (
          <div key={s.name} class="li" style={{ padding: '8px 0', flexWrap: 'wrap' }}>
            <span class="badge">{t('catalog.detail.needs.secret')}</span>
            <div class="grow mono" style={{ color: 'var(--ink)' }}>
              {s.name}
            </div>
            {!s.configured && (
              <a class="btn sm" href={href('/keys')} onClick={props.onClose}>
                {t('catalog.detail.needs.configure')}
              </a>
            )}
            {s.configured ? (
              <span class="badge ok">{t('catalog.secret.configured')}</span>
            ) : (
              <span class="badge warn">{t('catalog.secret.missing')}</span>
            )}
          </div>
        ))}
        {local.map((c) => (
          <div key={c.name} class="li" style={{ padding: '8px 0', flexWrap: 'wrap' }}>
            <span class="badge">{t('catalog.detail.needs.local')}</span>
            <div class="grow mono" style={{ color: 'var(--ink)' }}>
              {c.name}
            </div>
            {c.description && <span class="faint t12">{c.description}</span>}
          </div>
        ))}
      </div>
      {local.length > 0 && <p class="hint">{t('catalog.detail.needs.local.hint')}</p>}
    </div>
  )
}

const INSTALL: Record<Schemas['InstallStatus'], [MessageKey, string]> = {
  installed: ['catalog.install.installed', 'ok'],
  failed: ['catalog.install.failed', 'bad'],
  skipped: ['catalog.install.skipped', 'warn'],
  removed: ['catalog.install.removed', ''],
}

/** Installs is 在我的电脑上: where the resource is, per computer and AI tool, at the last sync. */
function Installs(props: { r: Schemas['CatalogResource']; fresh: boolean; onClose: () => void }) {
  const { r } = props
  return (
    <div>
      <div class="sect-t">{t('catalog.detail.mine')}</div>
      {props.fresh ? (
        <p class="dim t13">
          {t('catalog.detail.mine.fresh')}{' '}
          <a class="link" href={href('/connect')} onClick={props.onClose}>
            {t('catalog.detail.mine.connect')}
          </a>
        </p>
      ) : r.my_installs.length === 0 ? (
        <p class="dim t13">{r.enabled ? t('catalog.detail.mine.next_sync') : t('catalog.detail.mine.off')}</p>
      ) : (
        <div class="list">
          {r.my_installs.map((x) => {
            const [label, tone] = INSTALL[x.status]
            return (
              <div key={x.device_id + x.tool} class="li" style={{ padding: '8px 0' }}>
                <div class="grow">
                  {toolName(x.tool)} <span class="faint t12">· {x.device_name}</span>
                  {x.degraded && (
                    <>
                      {' '}
                      <span class="badge" title={t('catalog.install.degraded.why')}>
                        {t('catalog.install.degraded', { form: kindName(x.form ?? 'skill') })}
                      </span>
                    </>
                  )}
                </div>
                <span class={`badge ${tone}`.trim()}>
                  {x.status === 'installed' && <span class="dot" />}
                  {t(label, { version: x.version })}
                </span>
              </div>
            )
          })}
        </div>
      )}
      {!props.fresh && (
        <p class="hint">
          {t('catalog.detail.mine.status')} <span class="mono">{`${BRAND.cli} status ${r.code}`}</span>
        </p>
      )}
    </div>
  )
}
