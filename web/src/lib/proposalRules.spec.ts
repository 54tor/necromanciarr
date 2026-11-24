import { describe, expect, it } from 'vitest'
import { proposal } from '@/test/fixtures'
import {
  canAccept,
  canAcceptGroup,
  canChoose,
  canForce,
  canReject,
  decisionRank,
  groupMembers,
  hasSourceField,
  isSelectable,
} from './proposalRules'

describe('proposalRules', () => {
  it('accepts a pending or failed current proposal, never a conflict', () => {
    expect(canAccept(proposal({ decision: 'pending' }))).toBe(true)
    expect(canAccept(proposal({ decision: 'failed' }))).toBe(true)
    expect(canAccept(proposal({ decision: 'accepted' }))).toBe(false)
    expect(canAccept(proposal({ kind: 'conflict' }))).toBe(false)
    expect(canAccept(proposal({ current: false }))).toBe(false)
  })

  it('rejects only from pending, like the server', () => {
    expect(canReject(proposal({ decision: 'pending' }))).toBe(true)
    expect(canReject(proposal({ decision: 'failed' }))).toBe(false)
    expect(canReject(proposal({ current: false }))).toBe(false)
  })

  it('offers a free selection only on pending current rows', () => {
    expect(isSelectable(proposal({ decision: 'pending' }))).toBe(true)
    expect(isSelectable(proposal({ decision: 'accepted' }))).toBe(false)
    expect(isSelectable(proposal({ current: false }))).toBe(false)
  })

  it('chooses only on an open conflict', () => {
    expect(canChoose(proposal({ kind: 'conflict' }))).toBe(true)
    expect(canChoose(proposal({ kind: 'full' }))).toBe(false)
  })

  it('accepts a group only from an open primary', () => {
    expect(canAcceptGroup(proposal({ primary: true }))).toBe(true)
    expect(canAcceptGroup(proposal({ primary: false }))).toBe(false)
    expect(canAcceptGroup(proposal({ primary: true, decision: 'applied' }))).toBe(false)
  })

  it('forces only an applied partial annex seeded paused and current', () => {
    const allowed = proposal({
      kind: 'partial_annex',
      decision: 'applied',
      step: 'seeded_paused_incomplete',
    })

    expect(canForce(allowed)).toBe(true)
    expect(canForce({ ...allowed, kind: 'partial_pack' })).toBe(false)
    expect(canForce({ ...allowed, step: 'linked' })).toBe(false)
    expect(canForce({ ...allowed, decision: 'accepted' })).toBe(false)
    expect(canForce({ ...allowed, current: false })).toBe(false)
  })

  it('lists the open torrent proposals of one group, primary first', () => {
    const primary = proposal({ id: 1, group_id: 5, primary: true })
    const member = proposal({ id: 2, group_id: 5, primary: false })
    const otherGroup = proposal({ id: 3, group_id: 6 })
    const conflict = proposal({ id: 4, group_id: 5, kind: 'conflict', primary: false })
    const closed = proposal({ id: 5, group_id: 5, decision: 'rejected', primary: false })

    const gotIds = groupMembers([member, otherGroup, conflict, closed, primary], 5).map(
      (row) => row.id,
    )

    expect(gotIds).toEqual([1, 2])
  })

  it('ranks the decisions that wait for an action first', () => {
    expect(decisionRank('pending')).toBeLessThan(decisionRank('accepted'))
    expect(decisionRank('accepted')).toBeLessThan(decisionRank('superseded'))
  })

  it('detects the source field only when a row carries it', () => {
    expect(hasSourceField([proposal()])).toBe(false)
    expect(hasSourceField([proposal(), proposal({ source: 'library' })])).toBe(true)
  })
})
