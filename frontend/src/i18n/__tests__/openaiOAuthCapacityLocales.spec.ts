import { describe, expect, it } from 'vitest'
import en from '../locales/en'
import zh from '../locales/zh'

describe('OpenAI OAuth capacity locale keys', () => {
  it('exposes the navigation and page copy in Chinese', () => {
    expect(zh.nav.openaiOAuthCapacity).toBe('OpenAI OAuth 额度')
    expect(zh.admin.openaiOAuthCapacity).toMatchObject({
      title: 'OpenAI OAuth 额度',
      parentRemaining: '主周期剩余额度',
      fiveHourRemaining: '5h 剩余额度',
      weeklyRemaining: '周剩余额度',
      monthlyRemaining: '月剩余额度',
      allGroups: '全部分组',
      ungrouped: '未分组',
      groupOverview: '分组概览'
    })
  })

  it('exposes the navigation and page copy in English', () => {
    expect(en.nav.openaiOAuthCapacity).toBe('OpenAI OAuth Capacity')
    expect(en.admin.openaiOAuthCapacity).toMatchObject({
      title: 'OpenAI OAuth Capacity',
      parentRemaining: 'Primary window remaining',
      fiveHourRemaining: '5-hour remaining',
      weeklyRemaining: 'Weekly remaining',
      monthlyRemaining: 'Monthly remaining',
      allGroups: 'All groups',
      ungrouped: 'Ungrouped',
      groupOverview: 'Group overview'
    })
  })
})
