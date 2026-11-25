// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// The terminal panel of the connection guide (the prototype's termPanel): the same connection seen at four
// moments - install, sign in, preview, sync - laid out like the product site's sync list. It is an
// illustration, not output from this computer, and says so.

import type { ComponentChildren } from 'preact'
import { useState } from 'preact/hooks'
import { BRAND } from '../../brand.gen'
import { t } from '../../i18n'
import { Icon } from '../../ui/icons'
import { slashName } from '../../ui/resource'
import { serverAddress } from './commands'

const CLI = BRAND.cli
const P = BRAND.prefix
const HOME = '~/' + BRAND.home

function Row(props: { k?: string; mark: string; path: string; note: string; cls?: string; i: number }) {
  return (
    <div class={`pl-r ${props.cls ?? ''}`.trim()} data-k={props.k} style={{ '--i': props.i }}>
      <i>{props.mark}</i>
      <code>{props.path}</code>
      <span>{props.note}</span>
    </div>
  )
}

function Line(props: { children: ComponentChildren; cls?: string }) {
  return <div class={props.cls ? `ln ${props.cls}` : 'ln'}>{props.children}</div>
}

function Prompt(props: { cmd: string }) {
  return (
    <div class="pl-cmd">
      <span>$</span>
      {props.cmd}
    </div>
  )
}

export function Terminal(props: { name: string; version?: string }) {
  const [term, setTerm] = useState(0)
  const version = props.version ?? '0.5.0'
  const tabs: [string, string][] = [
    [t('connect.term.t1'), `brew install ${BRAND.cliLong}`],
    [t('connect.term.t2'), `${CLI} login`],
    [t('connect.term.t3'), `${CLI} sync --plan`],
    [t('connect.term.t4'), `${CLI} sync`],
  ]
  const next = [
    t('connect.term.next1'),
    t('connect.term.next2'),
    t('connect.term.next3'),
    t('connect.term.again'),
  ]

  const out = [
    () => (
      <>
        <Prompt cmd={`brew install ${BRAND.cliLong}`} />
        <Line cls="d">
          {[
            t('connect.term.o1.l1'),
            '#',
            t('connect.term.o1.l2'),
            t('connect.term.o1.l3'),
            t('connect.term.o1.l4'),
            t('connect.term.o1.l5', { home: HOME }),
            '#',
            t('connect.term.o1.l6'),
            t('connect.term.o1.l7', { cli: CLI }),
          ].join('\n')}
        </Line>
      </>
    ),
    () => (
      <>
        <Line>
          <span class="ok">✓</span> {t('connect.term.o2.installed', { name: BRAND.cliLong, version })}
        </Line>
        <Prompt cmd={`${CLI} login ${serverAddress()}`} />
        <Line>{'  ' + t('connect.term.o2.browser')}</Line>
        <div class="pl-code">
          <span class="tag">{t('connect.term.o2.code')}</span>
          <b>KQ7M-2XHP</b>
          <span class="pl-wait">
            <i />
            {t('connect.term.o2.wait')}
          </span>
        </div>
      </>
    ),
    () => {
      let i = 0
      return (
        <>
          <Line>
            <span class="ok">✓</span> {t('connect.term.o3.signed', { name: props.name })}
          </Line>
          <Prompt cmd={`${CLI} sync --plan`} />
          <Line cls="d">
            {'  ' + t('connect.term.o3.tools', { tools: 'Claude Code 2.x · Codex 0.x · OpenCode 2.x' })}
          </Line>
          <div class="pl-sum">
            <b>{t('connect.term.o3.will')}</b>
            {t('connect.term.o3.total.1')}
            <span class="big">31</span>
            {t('connect.term.o3.total.2')}
          </div>
          <div class="pl-g">
            <div class="pl-gh">
              {t('connect.term.o3.new')}
              <span>{t('connect.term.o3.new.note', { prefix: P, count: 27 })}</span>
            </div>
            <Row
              k="skill"
              mark="+"
              path={`~/.claude/skills/${P}unit-testing/`}
              note={t('connect.term.dir')}
              i={i++}
            />
            <Row
              k="skill"
              mark="+"
              path={`~/.claude/skills/${P}task-development/`}
              note={t('connect.term.dir')}
              i={i++}
            />
            <Row
              k="workflow"
              mark="+"
              path={`~/.claude/commands/${P}task-to-submit.md`}
              note={t('connect.term.command')}
              i={i++}
            />
            <Row
              k="skill"
              mark="+"
              path={`~/.agents/skills/${P}unit-testing/`}
              note={t('connect.term.dir')}
              i={i++}
            />
            <Row
              k="workflow"
              mark="+"
              path={`~/.agents/skills/${P}task-to-submit/`}
              note={t('connect.term.as_skill')}
              i={i++}
            />
            <Row mark="…" path={t('connect.term.more', { count: 22 })} note="" cls="more" i={i++} />
          </div>
          <div class="pl-g">
            <div class="pl-gh w">
              {t('connect.term.o3.global')}
              <span>{t('connect.term.o3.backup', { home: HOME })}</span>
            </div>
            <Row
              k="mcp"
              mark="~"
              path="~/.claude.json"
              note={t('connect.term.o3.claude', { a: `${P}jira`, b: `${P}gitlab` })}
              cls="mod"
              i={i++}
            />
            <Row
              k="mcp"
              mark="~"
              path="~/.codex/config.toml"
              note={t('connect.term.o3.codex', { table: `[mcp_servers.${P}jira]` })}
              cls="mod"
              i={i}
            />
          </div>
          <div class="pl-end">
            <span>
              <b>{t('connect.term.o3.keep')}</b>
              {t('connect.term.o3.keep.what')}
            </span>
            <span>
              {t('connect.term.o3.run.1')} <span class="mono">{`${CLI} sync`}</span>
              {t('connect.term.o3.run.2')} <span class="mono">{`${CLI} sync --skip-global`}</span>
              {t('connect.term.o3.run.3')}
            </span>
          </div>
        </>
      )
    },
    () => {
      let i = 0
      return (
        <>
          <Prompt cmd={`${CLI} sync`} />
          <Line>
            {'  '}
            <span class="w">?</span> {t('connect.term.o4.ask')}{' '}
            <span class="b">{t('connect.term.o4.yes')}</span>
          </Line>
          <div class="pl-g">
            <Row k="mcp" mark="✓" path={t('connect.term.o4.mcp')} note="Jira · GitLab · Confluence" i={i++} />
            <Row
              k="skill"
              mark="✓"
              path={t('connect.term.o4.skill')}
              note={t('connect.term.o4.skill.note')}
              i={i++}
            />
            <Row
              k="workflow"
              mark="✓"
              path={t('connect.term.o4.workflow')}
              note={`${slashName('req-to-design')} · ${slashName('task-to-submit')}`}
              i={i++}
            />
            <Row k="agent" mark="✓" path={t('connect.term.o4.agent')} note="backend-developer" i={i++} />
          </div>
          <div class="pl-g">
            <div class="pl-gh w">
              {t('connect.term.o4.keys')}
              <span>{t('connect.term.o4.keys.note')}</span>
            </div>
            <Row k="mcp" mark="!" path="Confluence" note="CONFLUENCE_TOKEN" cls="warn" i={i++} />
            <Row k="mcp" mark="!" path="Figma" note="FIGMA_TOKEN" cls="warn" i={i} />
          </div>
          <div class="pl-sum">
            <b>{t('connect.term.o4.done')}</b>
            {t('connect.term.o4.wrote.1')}
            <span class="big">31</span>
            {t('connect.term.o4.wrote.2')} <span class="mono">{`${HOME}/manifest.json`}</span>
          </div>
          <div class="pl-end">
            <span>
              {t('connect.term.o4.undo')} <span class="mono">{`${CLI} uninstall`}</span>
            </span>
            <span>
              <b>{t('connect.term.o4.next')}</b>
              {'　'}
              {t('connect.term.o4.next.say', { say: t('home.guide.say') })}
            </span>
          </div>
        </>
      )
    },
  ]

  return (
    <div class="plan">
      <div class="plan-tabs" role="tablist" aria-label={t('connect.term.label')}>
        {tabs.map(([title, cmd], i) => (
          <button
            key={i}
            type="button"
            role="tab"
            aria-selected={term === i}
            class={i < term ? 'done' : undefined}
            onClick={() => setTerm(i)}
          >
            <span class="tag">
              {i + 1} {title}
            </span>
            <code>{cmd}</code>
          </button>
        ))}
      </div>
      <div class="plan-out" role="tabpanel" aria-live="polite">
        {out[term]?.()}
      </div>
      <div class="plan-foot">
        <span class="tag">{t('connect.term.note')}</span>
        <div class="row">
          <button
            type="button"
            class="btn ghost icon"
            onClick={() => setTerm(0)}
            aria-label={t('connect.term.reset')}
            title={t('connect.term.reset')}
          >
            <Icon name="reset" />
          </button>
          <button type="button" class="btn primary" onClick={() => setTerm(term >= 3 ? 0 : term + 1)}>
            {next[term]}
            {term < 3 && (
              <span class="arr">
                <Icon name="arrow" />
              </span>
            )}
          </button>
        </div>
      </div>
    </div>
  )
}
