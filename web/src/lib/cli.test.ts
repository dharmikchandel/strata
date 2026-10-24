import { describe, expect, it } from 'vitest';
import { cliCommand, cliOutput } from './cli';
import type { SearchResult } from './api';

describe('cliCommand', () => {
  it('spells the example as a stratactl command with RFC 3339 times', () => {
    expect(cliCommand({ title: '', note: '', text: 'hangtest', tags: null, from: '', to: '', limit: 20 }, 3)).toBe('stratactl search -text hangtest -limit 3');
    expect(
      cliCommand({ title: '', note: '', text: 'parity error', tags: { level: 'FATAL' }, from: '2005-06-20T13:00', to: '2005-06-20T14:00', limit: 20 }, 3),
    ).toBe('stratactl search -text "parity error" -tag level=FATAL -from 2005-06-20T13:00:00Z -to 2005-06-20T14:00:00Z -limit 3');
  });
});

describe('cliOutput', () => {
  it('matches the format of cmd/stratactl/search.go', () => {
    const r: SearchResult = {
      hits: [{ time: '2005-06-20 13:44:57.111650', message: 'RAS APP FATAL ciod: Error', tags: { level: 'FATAL', component: 'APP' }, segment_id: 'x' }],
      truncated: true,
      metrics: { segments_considered: 73, skipped_by_time: 0, skipped_by_bloom: 71, segments_scanned: 2, bytes_read: 27682406, retries: 0, server_micros: 133452 },
      round_trip_ms: 139.24,
      histogram: null,
      segments: [],
      segments_truncated: false,
    };
    expect(cliOutput(r)).toEqual([
      '2005-06-20T13:44:57.111650Z  RAS APP FATAL ciod: Error  map[component:APP level:FATAL]',
      '... more lines match; showing the earliest 1 (raise -limit)',
      '',
      'segments  considered 73 | skipped by time 0 | skipped by bloom filter 71 | scanned 2',
      '          [' + '-'.repeat(58) + '#' + ']  . time  - bloom  # scanned',
      'read      26.4 MiB from storage | server time 133.45ms | round trip 139.2ms',
    ]);
  });
});
