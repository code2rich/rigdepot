// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// A-03 接入指南 (the prototype's V.connect): install the command line (the installer from this server, or
// Homebrew / Scoop: docs/architecture/13-open-questions.md D10), sign in, preview, sync. The terminal panel
// is an illustration of the four moments, as in the prototype; the steps and "我的电脑上的 AI 工具" come
// from my real computers (GET /api/v1/me/devices). Commands that arrive after M1 (daemon, privacy,
// environment resources, the browser helper) are left out.

import type { ComponentChildren } from 'preact'
import { useState } from 'preact/hooks'
import { api, unwrap, type Schemas } from '../../api/client'
import { BRAND } from '../../brand.gen'
import { fmtRelative } from '../../i18n/format'
import { t } from '../../i18n'
import { me } from '../../shell/session'
import { Icon } from '../../ui/icons'
import { LoadError, Loading, useLoad } from '../../ui/load'
import { Modal } from '../../ui/overlay'
import { PageHeader } from '../../ui/page-header'
import { toolName } from '../../ui/resource'
import { AiTip, Cmd, SelfTip, StatLi } from '../../ui/say'
import { installCommand, loginCommand } from './commands'
import { Terminal } from './terminal'
import './connect.css'

const CLI = BRAND.cli

export default function Connect() {
  const data = useLoad(async () => {
    const [devices, downloads] = await Promise.all([
      api.GET('/api/v1/me/devices').then(unwrap),
      api.GET('/api/v1/cli/downloads').then(
        (r) => r.data ?? null,
        () => null,
      ),
    ])
    return { devices: devices.items, downloads }
  })
  const [uninstall, setUninstall] = useState(false)
  const devices = data.data?.devices ?? []
  const connected = devices.length > 0
  const synced = devices.some((d) => d.last_sync)
  const reduce = typeof matchMedia === 'function' && matchMedia('(prefers-reduced-motion: reduce)').matches

  return (
    <>
      <PageHeader
        page="connect"
        title={t('nav.connect')}
        lead={t('connect.lead')}
        actions={
          <button
            type="button"
            class="btn"
            onClick={() =>
              document.getElementById('how')?.scrollIntoView({ behavior: reduce ? 'auto' : 'smooth' })
            }
          >
            {t('connect.how.go')}
            <span class="arr">
              <Icon name="arrow" />
            </span>
          </button>
        }
      />
      <div class="grid g-main">
        <div class="grid" style={{ alignContent: 'start' }}>
          <section class="card pad steps-v" aria-label={t('connect.steps.label')}>
            <Step n={1} done={connected} title={t('connect.s1')}>
              <p class="faint t13" style={{ marginBottom: '10px' }}>
                {t('connect.s1.desc')}
              </p>
              <Cmd text={installCommand(data.data?.downloads)} />
              <Cmd text={t('connect.s1.pkg', { pkg: BRAND.cliLong })} />
            </Step>
            <Step n={2} done={connected} title={t('connect.s2')}>
              <p class="faint t13" style={{ marginBottom: '10px' }}>
                {t('connect.s2.desc')}
              </p>
              <Cmd text={loginCommand()} />
            </Step>
            <Step n={3} done={synced} title={t('connect.s3')}>
              <p class="faint t13" style={{ marginBottom: '10px' }}>
                {t('connect.s3.desc.1')} <span class="mono">~/.claude.json</span>
                {t('connect.s3.desc.2')} <span class="mono">{`${CLI} sync`}</span>
                {t('connect.s3.desc.3')}
              </p>
              <Cmd text={`${CLI} sync --plan`} />
            </Step>
            <Step n={4} done={false} title={t('connect.s4')}>
              <div style={{ marginTop: '8px' }}>
                <AiTip
                  say={t('home.guide.say')}
                  note={t('connect.s4.note')}
                  label={t('home.guide.say.label')}
                />
              </div>
            </Step>
          </section>
          <section aria-label={t('connect.term.label')}>
            <Terminal name={me.value?.display_name ?? ''} version={data.data?.downloads?.version} />
          </section>
        </div>
        <div class="grid" style={{ alignContent: 'start' }}>
          <section class="card pad">
            <h3>
              {t('connect.tools.title')}
              <span class="tag">
                {data.state === 'ready'
                  ? connected
                    ? t('connect.tools.devices', { count: devices.length })
                    : t('connect.tools.none')
                  : ''}
              </span>
            </h3>
            {data.state === 'loading' && <Loading />}
            {data.state === 'failed' && <LoadError error={data.error} retry={data.reload} />}
            {data.state === 'ready' && <Tools devices={devices} />}
            <p class="hint">
              {t('connect.tools.hint.1')} <span class="mono">{`${CLI} sync --plan`}</span>{' '}
              {t('connect.tools.hint.2')}
            </p>
          </section>
          <section class="card pad">
            <h3>{t('connect.undo.title')}</h3>
            <p class="dim t13" style={{ marginBottom: '10px' }}>
              {t('connect.undo.desc')}
            </p>
            <Cmd text={`${CLI} uninstall --plan`} />
            <Cmd text={`${CLI} uninstall`} />
            <p class="hint">
              {t('connect.undo.tool')} <span class="mono">{`${CLI} uninstall --tool codex`}</span>
              {t('connect.undo.kept', { prefix: BRAND.prefix })}
            </p>
            <button
              type="button"
              class="btn"
              style={{ marginTop: '10px' }}
              onClick={() => setUninstall(true)}
            >
              <Icon name="eye" />
              {t('connect.undo.show')}
            </button>
          </section>
        </div>
      </div>
      <Commands />
      <AiCan />
      <How />
      {uninstall && <UninstallDialog onClose={() => setUninstall(false)} />}
    </>
  )
}

function Step(props: { n: number; done: boolean; title: string; children: ComponentChildren }) {
  return (
    <div class={props.done ? 'step done' : 'step'}>
      <div class="n">{props.done ? <Icon name="check" /> : props.n}</div>
      <div class="grow">
        <h3>
          {props.title}
          {props.done && <span class="sr">{t('connect.step.done')}</span>}
        </h3>
        {props.children}
      </div>
    </div>
  )
}

/** Tools lists the AI tools each of my computers reported at its last sync. */
function Tools(props: { devices: Schemas['Device'][] }) {
  if (props.devices.length === 0) {
    return (
      <div>
        <StatLi label={t('connect.tools.title.short')} tone="off">
          {t('connect.tools.later')}
        </StatLi>
      </div>
    )
  }
  return (
    <div>
      {props.devices.map((d) =>
        d.tools.length === 0 ? (
          <StatLi key={d.id} label={d.name} tone="off">
            {t('connect.tools.found_none')}
          </StatLi>
        ) : (
          d.tools.map((tool) => (
            <StatLi key={d.id + tool.tool} label={toolName(tool.tool)} tone="ok">
              {t('connect.tools.on')}
              {tool.version && (
                <>
                  {' · '}
                  <span class="mono">{tool.version}</span>
                </>
              )}
              <span class="faint">
                {' · '}
                {d.name}
                {d.last_sync && ` · ${fmtRelative(d.last_sync.at)}`}
              </span>
            </StatLi>
          ))
        ),
      )}
    </div>
  )
}

function Commands() {
  const rows: [string, string][] = [
    [`${CLI} sync --plan`, t('connect.cmd.plan')],
    [`${CLI} sync`, t('connect.cmd.sync')],
    [`${CLI} status`, t('connect.cmd.status')],
    [`${CLI} uninstall`, t('connect.cmd.uninstall')],
    [`${CLI} enable|disable <code>`, t('connect.cmd.enable')],
    [`${CLI} secret set <NAME>`, t('connect.cmd.secret')],
    [`${CLI} feedback`, t('connect.cmd.feedback')],
    [`${CLI} doctor`, t('connect.cmd.doctor')],
  ]
  return (
    <section class="card" style={{ marginTop: '16px' }}>
      <div class="card-h">
        <h3>{t('connect.cmd.title')}</h3>
        <span class="tag">{`${CLI} --help`}</span>
      </div>
      <div class="tbl-wrap">
        <table>
          <tbody>
            {rows.map(([c, d]) => (
              <tr key={c}>
                <td class="code cmd-col">{c}</td>
                <td class="dim t12">{d}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  )
}

function AiCan() {
  const can: [string, string][] = [
    [t('connect.ai.see'), t('home.guide.say')],
    [t('connect.ai.config'), t('connect.ai.config.say')],
    [t('connect.ai.feedback'), t('connect.ai.feedback.say')],
  ]
  const nod: [string, string][] = [
    [t('connect.ai.publish'), t('connect.ai.publish.say')],
    [t('connect.ai.manage'), t('connect.ai.manage.say')],
  ]
  return (
    <section class="card pad" style={{ marginTop: '16px' }}>
      <h3>{t('connect.ai.title')}</h3>
      <div class="grid g2" style={{ gap: '20px', alignItems: 'start' }}>
        <div style={{ display: 'grid', gap: '8px' }}>
          <div class="sect-t" style={{ margin: 0 }}>
            {t('connect.ai.can')}
          </div>
          {can.map(([label, say]) => (
            <AiTip key={label} say={say} label={label} />
          ))}
        </div>
        <div style={{ display: 'grid', gap: '8px' }}>
          <div class="sect-t" style={{ margin: 0 }}>
            {t('connect.ai.nod')}
          </div>
          {nod.map(([label, say]) => (
            <AiTip key={label} say={say} label={label} />
          ))}
          <p class="hint" style={{ margin: 0 }}>
            {t('connect.ai.nod.hint')}
          </p>
          <SelfTip>
            <b>{t('connect.ai.self.title')}</b>
            {t('connect.ai.self')}
          </SelfTip>
        </div>
      </div>
    </section>
  )
}

function How() {
  return (
    <section class="card pad" id="how" style={{ marginTop: '16px', scrollMarginTop: '64px' }}>
      <h3>
        {t('connect.how.title')}{' '}
        <span class="faint t12" style={{ fontWeight: 400 }}>
          {t('connect.how.sub')}
        </span>
      </h3>
      <div class="arch">
        <div class="zone">
          <div class="zone-t">
            <span class="tag">A</span>
            <b class="t13">{t('connect.how.a', { product: BRAND.name })}</b>
          </div>
          <div class="nd">
            <b>{t('connect.how.a1')}</b>
            <span>{t('connect.how.a1.d')}</span>
          </div>
          <div class="nd">
            <b>{t('connect.how.a2')}</b>
            <span>{t('connect.how.a2.d')}</span>
          </div>
          <div class="nd">
            <b>{t('connect.how.a3')}</b>
            <span>{t('connect.how.a3.d')}</span>
          </div>
          <div class="nd">
            <b>{t('connect.how.a4')}</b>
            <span>{t('connect.how.a4.d')}</span>
          </div>
          <div class="none">{t('connect.how.none')}</div>
        </div>
        <div class="pipes">
          <div class="pipe">{t('connect.how.p1')}</div>
          <div class="pipe back">{t('connect.how.p4')}</div>
        </div>
        <div class="zone me">
          <div class="zone-t">
            <span class="tag">B</span>
            <b class="t13">{t('connect.how.b')}</b>
          </div>
          <div class="nd core">
            <b>{t('connect.how.b1', { cli: CLI })}</b>
            <span>{t('connect.how.b1.d')}</span>
          </div>
          <div class="nd">
            <b>{t('connect.how.b2')}</b>
            <span>{t('connect.how.b2.d', { prefix: BRAND.prefix })}</span>
          </div>
        </div>
        <div class="pipes">
          <div class="pipe">{t('connect.how.p3')}</div>
        </div>
        <div class="zone">
          <div class="zone-t">
            <span class="tag">C</span>
            <b class="t13">{t('connect.how.c')}</b>
          </div>
          <div class="nd">
            <b>{t('connect.how.c1')}</b>
            <span>{t('connect.how.c1.d')}</span>
          </div>
          <div class="nd">
            <b>{t('connect.how.c2')}</b>
            <span>{t('connect.how.c2.d')}</span>
          </div>
        </div>
      </div>
      <div class="callout info" style={{ marginTop: '16px' }}>
        <b>{t('connect.how.repo.title')}</b>
        <span class="dim" style={{ minWidth: 0, overflowWrap: 'anywhere' }}>
          {t('connect.how.repo.1')}{' '}
          <span class="mono">{`${CLI} sync --repo https://gitlab.example.com/platform/harness-resources.git`}</span>
          {t('connect.how.repo.2')}
        </span>
      </div>
    </section>
  )
}

/** UninstallDialog shows what `rgd uninstall` does, as an illustration of its preview. */
function UninstallDialog(props: { onClose: () => void }) {
  const p = BRAND.prefix
  const home = '~/' + BRAND.home
  const text = [
    `$ ${CLI} uninstall --plan`,
    `  ${t('connect.undo.d.head', { home })}`,
    '',
    `  - ~/.claude/skills/${p}*/            ${t('connect.undo.d.dirs', { count: 9 })}`,
    `  - ~/.claude/commands/${p}*.md        ${t('connect.undo.d.files', { count: 2 })}`,
    `  ~ ~/.claude.json                     ${t('connect.undo.d.keys', { key: `mcpServers.${p}jira`, count: 3 })}`,
    `                                       ${t('connect.undo.d.keep')}`,
    `  ~ ~/.codex/config.toml               ${t('connect.undo.d.tables', { table: `[mcp_servers.${p}*]`, count: 3 })}`,
    `  - ~/.agents/skills/${p}*/            ${t('connect.undo.d.dirs', { count: 7 })}`,
    '',
    `  ! ~/.claude/skills/${p}unit-testing/SKILL.md`,
    `    ${t('connect.undo.d.changed', { home })}`,
    '',
    `  ${t('connect.undo.d.never')}`,
    `  ${t('connect.undo.d.last', { home })}`,
  ].join('\n')
  return (
    <Modal
      title={t('connect.undo.d.title', { cli: CLI })}
      titleClass="mono"
      wide
      onClose={props.onClose}
      foot={
        <button type="button" class="btn" onClick={props.onClose}>
          {t('ui.got_it')}
        </button>
      }
    >
      <pre class="code">{text}</pre>
      <p class="hint" style={{ margin: 0 }}>
        {t('connect.undo.d.note')}
      </p>
    </Modal>
  )
}
