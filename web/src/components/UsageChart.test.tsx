import { fireEvent, render, screen, within } from '@testing-library/react'
import { expect, it } from 'vitest'
import { UsageChart } from './UsageChart'

const format = (value: number) => String(value)

it('exposes daily values to the keyboard and leaves missing token reports unknown', () => {
  render(
    <UsageChart
      points={[
        { id: '1900000000', amount: 25, partial: false },
        { id: '1900086400', amount: null, partial: true },
      ]}
      label="Usage trend · Tokens"
      metricLabel="Tokens"
      format={format}
      axis={format}
    />,
  )
  const chart = screen.getByRole('img', { name: 'Usage trend · Tokens' })
  fireEvent.keyDown(chart, { key: 'Home' })
  expect(within(screen.getByRole('status')).getByText('25')).toBeTruthy()
  fireEvent.keyDown(chart, { key: 'ArrowRight' })
  expect(
    within(screen.getByRole('status')).getByText('Not reported'),
  ).toBeTruthy()
  expect(screen.getByText('Missing token reports appear as gaps.')).toBeTruthy()
})

it('does not turn entirely missing usage into a zero-valued chart', () => {
  render(
    <UsageChart
      points={[
        { id: '1900000000', amount: null, partial: true },
        { id: '1900086400', amount: 0, partial: false },
      ]}
      label="Usage trend · Tokens"
      metricLabel="Tokens"
      format={format}
      axis={format}
    />,
  )
  expect(
    screen.getByText('No values were reported for this metric in this period.'),
  ).toBeTruthy()
  expect(screen.queryByRole('img')).toBeNull()
})
