import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { expect, it, vi } from 'vitest'
import { SchemeForm } from './SchemeForm'
import { AllocationBalances } from './AllocationBalances'
import type { AllocationDetail } from '@/lib/allocations'

const keys = [
  { id: 4, name: 'Synthetic holder', prefix: 'sl_synth', owner: 'owner-test' },
  { id: 5, name: 'Synthetic other', prefix: 'sl_other', owner: 'owner-test' },
]

function mountKeyShareForm() {
  const submit = vi.fn()
  render(
    <SchemeForm
      groups={[{ id: 2, name: 'Synthetic pool', enabled: true }]}
      groupID={2}
      onGroupChange={() => {}}
      members={[]}
      keys={keys}
      onSubmit={submit}
      onCancel={() => {}}
      pending={false}
    />,
  )
  return submit
}

it('gives selected keys a share of every required window budget', async () => {
  const user = userEvent.setup()
  const submit = mountKeyShareForm()
  await user.type(screen.getByLabelText('Resource allowance name'), 'Shared')
  await user.click(screen.getByLabelText('By key share'))
  expect(screen.queryByLabelText('Start next period')).toBeNull()
  await user.click(screen.getByRole('button', { name: 'Add condition' }))
  await user.click(screen.getByRole('button', { name: 'Add condition' }))
  await user.type(screen.getByLabelText('Duration for condition 1'), '5')
  await user.type(screen.getByLabelText('Duration for condition 2'), '7')
  await user.selectOptions(
    screen.getByLabelText('Unit for condition 2'),
    'days',
  )
  await user.type(screen.getByLabelText('Budget for condition 1'), '10')
  await user.click(
    screen.getByRole('checkbox', {
      name: 'Limit Synthetic holder (sl_synth…)',
    }),
  )
  await user.type(
    screen.getByLabelText('5 hours share for Synthetic holder (sl_synth…)'),
    '100',
  )
  await user.type(
    screen.getByLabelText('7 days share for Synthetic holder (sl_synth…)'),
    '40.5',
  )
  const save = screen.getByRole('button', { name: 'Save resource allowance' })
  await user.click(save)
  expect(submit).not.toHaveBeenCalled()
  expect(screen.getByRole('alert')).toBeTruthy()
  await user.type(screen.getByLabelText('Budget for condition 2'), '100')
  await user.click(save)
  expect(submit.mock.calls[0][0].config).toEqual({
    mode: 'keys',
    period: 'durations',
    members: [],
    rates: [],
    windows: [
      { duration_seconds: 5 * 3600, limit: 10_000_000 },
      { duration_seconds: 7 * 86400, limit: 100_000_000 },
    ],
    keys: [
      {
        key_id: 4,
        shares: [
          { duration_seconds: 5 * 3600, share: 10000 },
          { duration_seconds: 7 * 86400, share: 4050 },
        ],
      },
    ],
  })
})

it('rejects a key share above 100%', async () => {
  const user = userEvent.setup()
  const submit = mountKeyShareForm()
  await user.type(screen.getByLabelText('Resource allowance name'), 'Shared')
  await user.click(screen.getByLabelText('By key share'))
  await user.click(screen.getByRole('button', { name: 'Add condition' }))
  await user.type(screen.getByLabelText('Duration for condition 1'), '5')
  await user.type(screen.getByLabelText('Budget for condition 1'), '10')
  await user.click(
    screen.getByRole('checkbox', { name: 'Limit Synthetic other (sl_other…)' }),
  )
  await user.type(
    screen.getByLabelText('5 hours share for Synthetic other (sl_other…)'),
    '120',
  )
  await user.click(
    screen.getByRole('button', { name: 'Save resource allowance' }),
  )
  expect(submit).not.toHaveBeenCalled()
})

function keyDetail(
  overrides: Partial<AllocationDetail> = {},
  balances: AllocationDetail['balances'] = [],
): AllocationDetail {
  return {
    id: 1,
    name: 'Synthetic key shares',
    group_id: 2,
    group_name: 'Synthetic pool',
    enabled: true,
    created_at: 1,
    effective_at: 1,
    next: null,
    config: {
      mode: 'keys',
      period: 'durations',
      members: [],
      rates: [],
      windows: [{ duration_seconds: 5 * 3600, limit: 10_000_000 }],
      keys: [],
    },
    available: true,
    pending: [],
    balances,
    ...overrides,
  }
}

const exhausted: AllocationDetail['balances'][number] = {
  user_id: 0,
  username: '',
  key_id: 4,
  key_name: 'Synthetic holder',
  key_prefix: 'sl_synth',
  share: 4000,
  window_kind: 'trailing',
  window_seconds: 5 * 3600,
  mode: 'amount',
  limit: 4_000_000,
  used: 4_000_000,
  tokens: 1000,
  pending: 0,
  pending_current: 0,
  in_flight: 0,
  reserved: 0,
  admission_room: 0,
  admission: 'exhausted',
  reset_at: 2_000_000_000,
}

it('reports each key share and when an exhausted key recovers', () => {
  render(<AllocationBalances detail={keyDetail({}, [exhausted])} />)
  expect(
    screen.getByRole('heading', { name: 'Synthetic holder (sl_synth…)' }),
  ).toBeTruthy()
  expect(screen.getByText('Allowance exhausted')).toBeTruthy()
  expect(screen.getByText('5 hours · Share 40%')).toBeTruthy()
  expect(screen.getByText(/Usable again around/)).toBeTruthy()
})

it('labels a paused key-share allowance as not limiting', () => {
  render(
    <AllocationBalances
      detail={keyDetail({ enabled: false, available: false }, [
        { ...exhausted, admission: 'active', used: 0, reset_at: 0 },
      ])}
    />,
  )
  expect(screen.getByText('Not limiting')).toBeTruthy()
  expect(screen.getByText(/these keys are not limited/)).toBeTruthy()
  expect(screen.getByText(/Trailing window/)).toBeTruthy()
})

it('shows an honest empty state before any key is selected', () => {
  render(<AllocationBalances detail={keyDetail()} />)
  expect(screen.getByText('No keys selected yet.')).toBeTruthy()
})
