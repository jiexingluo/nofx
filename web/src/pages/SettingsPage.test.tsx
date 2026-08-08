import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { SettingsPage } from './SettingsPage'
import type { DecisionRecord, TraderInfo } from '../types'

const mocks = vi.hoisted(() => ({
  getTraders: vi.fn(),
  getLatestDecisions: vi.fn(),
  getModelConfigs: vi.fn(),
  getSupportedModels: vi.fn(),
  getExchangeConfigs: vi.fn(),
  getExchangeAccountState: vi.fn(),
}))

vi.mock('../contexts/AuthContext', () => ({
  useAuth: () => ({ user: { email: 'owner@example.com' } }),
}))

vi.mock('../contexts/LanguageContext', () => ({
  useLanguage: () => ({ language: 'en' }),
}))

vi.mock('../lib/api', () => ({
  api: {
    getTraders: mocks.getTraders,
    getLatestDecisions: mocks.getLatestDecisions,
    getModelConfigs: mocks.getModelConfigs,
    getSupportedModels: mocks.getSupportedModels,
    getExchangeConfigs: mocks.getExchangeConfigs,
    getExchangeAccountState: mocks.getExchangeAccountState,
  },
}))

function makeTrader(id: string, name: string): TraderInfo {
  return { trader_id: id, trader_name: name, ai_model: 'qwen' }
}

function makeDecision(id: number, cycle: number): DecisionRecord {
  return {
    id,
    trader_id: 'trader-1',
    timestamp: '2026-08-08T12:00:00Z',
    cycle_number: cycle,
    system_prompt: 'system prompt text',
    input_prompt: 'input prompt text',
    cot_trace: '',
    decision_json: '',
    raw_response: 'raw response text',
    account_state: {
      total_balance: 0,
      available_balance: 0,
      total_unrealized_profit: 0,
      position_count: 0,
      margin_used_pct: 0,
      initial_balance: 0,
    },
    positions: [],
    candidate_coins: [],
    decisions: [],
    execution_log: [],
    success: true,
  }
}

describe('Settings page logs tab', () => {
  beforeEach(() => {
    mocks.getTraders.mockReset()
    mocks.getLatestDecisions.mockReset()
    mocks.getModelConfigs.mockReset().mockResolvedValue([])
    mocks.getSupportedModels.mockReset().mockResolvedValue([])
    mocks.getExchangeConfigs.mockReset().mockResolvedValue([])
    mocks.getExchangeAccountState.mockReset().mockResolvedValue({ states: {} })
  })

  it('shows an empty state and no trader picker when no trader is configured', async () => {
    mocks.getTraders.mockResolvedValue([])

    render(<SettingsPage />)
    fireEvent.click(screen.getByRole('button', { name: /Logs/i }))

    await waitFor(() => expect(mocks.getTraders).toHaveBeenCalled())
    expect(screen.getByText('No trader configured yet')).toBeTruthy()
    expect(mocks.getLatestDecisions).not.toHaveBeenCalled()
  })

  it('renders one card per decision without a trader picker when there is a single trader', async () => {
    mocks.getTraders.mockResolvedValue([makeTrader('trader-1', 'test')])
    mocks.getLatestDecisions.mockResolvedValue([
      makeDecision(1, 3),
      makeDecision(2, 2),
      makeDecision(3, 1),
    ])

    render(<SettingsPage />)
    fireEvent.click(screen.getByRole('button', { name: /Logs/i }))

    await waitFor(() =>
      expect(mocks.getLatestDecisions).toHaveBeenCalledWith('trader-1', 20)
    )
    expect(await screen.findAllByText(/Cycle #/)).toHaveLength(3)
    expect(screen.getByText('test')).toBeTruthy()
    // Only the page-size selector should exist — no trader picker for a
    // single trader.
    expect(screen.getAllByRole('combobox')).toHaveLength(1)
  })

  it('shows a trader picker when there is more than one trader', async () => {
    mocks.getTraders.mockResolvedValue([
      makeTrader('trader-1', 'test'),
      makeTrader('trader-2', 'second'),
    ])
    mocks.getLatestDecisions.mockResolvedValue([])

    render(<SettingsPage />)
    fireEvent.click(screen.getByRole('button', { name: /Logs/i }))

    await waitFor(() => expect(mocks.getLatestDecisions).toHaveBeenCalled())
    // Trader picker + page-size selector.
    expect(screen.getAllByRole('combobox')).toHaveLength(2)
  })
})
