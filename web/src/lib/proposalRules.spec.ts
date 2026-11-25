import { describe, expect, it } from 'vitest'
import { proposal } from '@/test/fixtures'
import {
  canAccept,
  canAcceptGroup,
  canChoose,
  canForce,
  canReject,
  decisionRank,
  groupTargets,
  hasNoAnnounce,
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

  it('accepts a group only from a pending or accepted full primary, like the server', () => {
    expect(canAcceptGroup(proposal({ primary: true }))).toBe(true)
    expect(canAcceptGroup(proposal({ primary: true, decision: 'accepted' }))).toBe(true)
    expect(canAcceptGroup(proposal({ primary: false }))).toBe(false)
    expect(canAcceptGroup(proposal({ primary: true, decision: 'applied' }))).toBe(false)
    expect(canAcceptGroup(proposal({ primary: true, decision: 'failed' }))).toBe(false)
    expect(canAcceptGroup(proposal({ primary: true, kind: 'partial_annex' }))).toBe(false)
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

  describe('groupTargets', () => {
    const primary = proposal({ id: 1, candidate_id: 10, primary: true })
    const crossSeed = proposal({ id: 2, candidate_id: 11, kind: 'cross_seed', primary: false })
    const fullOfSame = proposal({ id: 3, candidate_id: 11, kind: 'full', primary: false })
    const fullOfOther = proposal({ id: 4, candidate_id: 12, kind: 'full', primary: false })
    const failedMember = proposal({ id: 5, candidate_id: 13, decision: 'failed', primary: false })
    const conflict = proposal({ id: 6, candidate_id: 14, kind: 'conflict', primary: false })

    it('accepts only the primary when cross-seed is not native', () => {
      const gotIds = groupTargets([crossSeed, primary, fullOfOther], false).map((row) => row.id)

      expect(gotIds).toEqual([1])
    })

    it('adds one pending proposal per other candidate in native mode, cross_seed first', () => {
      const gotIds = groupTargets([fullOfSame, crossSeed, primary, fullOfOther], true).map(
        (row) => row.id,
      )

      expect(gotIds).toEqual([1, 2, 4])
    })

    it('never lists a failed member or a conflict', () => {
      const gotIds = groupTargets([primary, failedMember, conflict], true).map((row) => row.id)

      expect(gotIds).toEqual([1])
    })

    it('skips a candidate whose torrent is already accepted', () => {
      const taken = proposal({ id: 7, candidate_id: 11, decision: 'accepted', primary: false })

      const gotIds = groupTargets([primary, taken, crossSeed], true).map((row) => row.id)

      expect(gotIds).toEqual([1])
    })

    it('has no target without an open full primary', () => {
      expect(groupTargets([crossSeed, fullOfOther], true)).toEqual([])
    })
  })

  it('ranks the decisions that wait for an action first', () => {
    expect(decisionRank('pending')).toBeLessThan(decisionRank('accepted'))
    expect(decisionRank('accepted')).toBeLessThan(decisionRank('superseded'))
  })

  it('marks no announce only when the server says the source is none', () => {
    const withSource = (announceSource?: 'config' | 'indexer' | 'none') =>
      proposal({ details: { ...proposal().details, announce_source: announceSource } })
    expect(hasNoAnnounce(withSource('none'))).toBe(true)
    expect(hasNoAnnounce(withSource('config'))).toBe(false)
    expect(hasNoAnnounce(withSource('indexer'))).toBe(false)
    expect(hasNoAnnounce(withSource(undefined))).toBe(false)
  })
})
