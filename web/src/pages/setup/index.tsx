// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// The first-run wizard, from the prototype (V.setup in app.js): five steps, each one changeable later in
// the console. Only the first needs its own operation: creating the first administrator with the one-time
// setup token from the server log (D7), which also signs them in. The steps after it run as that
// administrator through the ordinary operations (sign-in methods, resource repositories, modules,
// products), and the last one closes the wizard (POST /setup/complete), after which /setup is gone.
//
// Each step renders its own <form id="wz-form">; the Next button at the foot submits it, so a step decides
// for itself what Next saves.

import type { ComponentType } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { useLocation } from 'preact-iso'
import { ApiError, api, unwrap, type Schemas } from '../../api/client'
import { t, type MessageKey } from '../../i18n'
import { href } from '../../router/paths'
import { me, meta } from '../../shell/session'
import { CenterCard } from '../../ui/center-card'
import { Fail } from '../../ui/fail'
import { Icon } from '../../ui/icons'
import { AdminStep, MethodsStep, ModulesStep, ProductStep, RepoStep, type StepProps } from './steps'
import './setup.css'

const STEPS: readonly { title: MessageKey; body: ComponentType<StepProps> }[] = [
  { title: 'setup.step.admin', body: AdminStep },
  { title: 'setup.step.methods', body: MethodsStep },
  { title: 'setup.step.repo', body: RepoStep },
  { title: 'setup.step.privacy', body: ModulesStep },
  { title: 'setup.step.product', body: ProductStep },
]

type Status =
  { at: 'loading' } | { at: 'failed'; error: unknown } | { at: 'open'; status: Schemas['SetupStatus'] }

export default function Setup() {
  const { route } = useLocation()
  const [state, setState] = useState<Status>({ at: 'loading' })
  const [step, setStep] = useState(0)
  const [reached, setReached] = useState(0)
  const [busy, setBusy] = useState(false)
  const admin = me.value?.platform_admin === true

  const finished = async () => {
    try {
      meta.value = unwrap(await api.GET('/api/v1/meta'))
    } catch {
      if (meta.value) meta.value = { ...meta.value, setup_complete: true }
    }
    route(href('/'), true)
  }

  const load = () => {
    setState({ at: 'loading' })
    api.GET('/api/v1/setup').then(
      (res) => {
        try {
          const status = unwrap(res)
          setState({ at: 'open', status })
          if (status.admin_created && me.value?.platform_admin) {
            setStep((s) => Math.max(s, 1))
            setReached((r) => Math.max(r, 1))
          }
        } catch (err) {
          if (err instanceof ApiError && err.code === 'setup.closed') void finished()
          else setState({ at: 'failed', error: err })
        }
      },
      (error: unknown) => setState({ at: 'failed', error }),
    )
  }
  useEffect(load, [])

  const go = (to: number) => {
    setStep(to)
    setReached((r) => Math.max(r, to))
  }
  const next = () => {
    if (step < STEPS.length - 1) go(step + 1)
    else void finished()
  }

  const last = step === STEPS.length - 1
  const current = STEPS[Math.min(step, STEPS.length - 1)] as (typeof STEPS)[number]
  const Body = current.body
  const open = state.at === 'open'
  const adminReady = open && state.status.admin_created && admin

  return (
    <CenterCard
      class="wz-card"
      after={<span class="tag wz-after">· {t('setup.tag')}</span>}
      tag={t('setup.progress', { n: step + 1, total: STEPS.length })}
      foot={
        <>
          <button type="button" class="btn ghost" disabled={step === 0 || busy} onClick={() => go(step - 1)}>
            {t('setup.back')}
          </button>
          <div class="row">
            {step === 2 && (
              <button type="button" class="btn ghost" disabled={busy} onClick={next}>
                {t('setup.skip')}
              </button>
            )}
            <button
              type="submit"
              form="wz-form"
              class="btn primary"
              disabled={busy || !open || (step > 0 && !adminReady)}
              aria-busy={busy}
            >
              {last ? t('setup.finish') : t('setup.next')}
              {!last && (
                <span class="arr">
                  <Icon name="arrow" />
                </span>
              )}
            </button>
          </div>
        </>
      }
    >
      <div class="wz-prog" aria-hidden="true">
        <i style={{ width: `${((step + 1) / STEPS.length) * 100}%` }} />
      </div>
      <div class="wizard">
        <ol class="steps click" aria-label={t('setup.steps')}>
          {STEPS.map((s, k) => {
            const can = k !== step && k <= reached && (k === 0 || adminReady) && !busy
            return (
              <li
                key={s.title}
                class={k < step ? 'done' : k === step ? 'cur' : ''}
                aria-current={k === step ? 'step' : undefined}
              >
                <button type="button" disabled={!can} onClick={() => go(k)}>
                  <span class="n">{k < step ? <Icon name="check" /> : `0${k + 1}`}</span>
                  <div>
                    <b>{t(s.title)}</b>
                  </div>
                </button>
              </li>
            )
          })}
        </ol>
        <div class="wz-body">
          <h2>
            <b>0{step + 1}</b>
            {t(current.title)}
          </h2>
          {state.at === 'loading' && (
            <p class="faint t13" aria-busy="true">
              {t('setup.loading')}
            </p>
          )}
          {state.at === 'failed' && <Fail error={state.error} retry={load} />}
          {open && (
            <Body
              key={step}
              status={state.status}
              setBusy={setBusy}
              done={next}
              created={(status) => {
                setState({ at: 'open', status })
                go(1)
              }}
            />
          )}
        </div>
      </div>
    </CenterCard>
  )
}
