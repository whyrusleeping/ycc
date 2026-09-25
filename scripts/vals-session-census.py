#!/usr/bin/env python3
"""Read-only JSONL census for the vals harness retrospective (standard library only).

Usage: python3 scripts/vals-session-census.py /path/to/vals/.ycc/sessions
Prints aggregates and per-log metadata, never raw prompts or tool output. This is
an investigation script, not a supported analytics API. Event counts are not
success rates; elapsed spans include human waits and reopened-session gaps.
"""
import argparse
import collections
import hashlib
import json
import math
import re
from datetime import datetime, timezone
from pathlib import Path


def counter():
    return collections.Counter()


def quantiles(values):
    values = sorted(values)
    if not values:
        return {}
    return {str(p): values[max(0, math.ceil(p / 100 * len(values)) - 1)]
            for p in (50, 90, 95, 99, 100)}


def timestamp(value):
    # Preserve RFC3339 offsets while reducing Go's nanosecond precision for
    # Python 3.10. Durations are measured to microseconds, never naive local time.
    match = re.fullmatch(r'(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(?:\.(\d+))?(Z|[+-]\d{2}:\d{2})', value)
    if not match:
        raise ValueError('Expected an RFC3339 timestamp with an explicit timezone')
    base, fraction, zone = match.groups()
    fraction = '.' + fraction[:6].ljust(6, '0') if fraction else ''
    zone = '+00:00' if zone == 'Z' else zone
    return datetime.fromisoformat(base + fraction + zone).astimezone(timezone.utc)


def census(root, before=None):
    events, modes, tools, errors, verdicts, error_kinds = [counter() for _ in range(6)]
    usage = collections.defaultdict(counter)
    turns = collections.defaultdict(counter)
    tool_bytes = counter()
    sessions = []
    notification_lags, question_waits = [], []
    digest = hashlib.sha256()
    cutoff = timestamp(before) if before else None
    for path in sorted(root.glob('*/events.jsonl')):
        raw = path.read_bytes()  # one bounded log snapshot; not a live subscription
        if before:
            selected = []
            for line in raw.splitlines(keepends=True):
                try:
                    if timestamp(json.loads(line)['ts']) < cutoff:
                        selected.append(line)
                except (ValueError, KeyError, UnicodeDecodeError):
                    raise ValueError('Cannot time-filter malformed log: ' + str(path) +
                                     '; a live append may be incomplete. Retry against complete/quiescent '
                                     'log snapshots; malformed records are not silently discarded.') from None
            raw = b''.join(selected)
            if not raw:
                continue
        sha = hashlib.sha256(raw).hexdigest()
        digest.update((path.parent.name + ':' + sha + '\n').encode())
        row = dict(id=path.parent.name, bytes=len(raw), sha256=sha,
                   events=counter(), tool_errors=counter(), invalid_lines=0,
                   max_context_est=0, model_duration_ms=0, tool_duration_ms=0,
                   tool_result_bytes=0, model_turns_with_tools=0,
                   single_tool_turns=0, max_implementer_round=0,
                   max_review_round=0, user_inputs=0)
        stamps = []
        finished_jobs, question_started = {}, None
        for line in raw.splitlines():
            try:
                e = json.loads(line)
            except (ValueError, UnicodeDecodeError):
                row['invalid_lines'] += 1
                continue
            t, d = e['type'], e.get('data') or {}
            row['events'][t] += 1
            events[t] += 1
            stamps.append(e['ts'])
            if t == 'session_started':
                row.setdefault('mode', d.get('mode', 'unknown'))
                row.setdefault('coordinator', d.get('coordinator', 'unknown'))
                modes[d.get('mode', 'unknown')] += 1
            elif t == 'user_input':
                row['user_inputs'] += 1
            elif t == 'model_turn':
                model = d.get('model_name') or d.get('model_id', 'unknown')
                if d.get('synthetic'):
                    turns[model]['synthetic_count'] += 1
                    continue
                turns[model]['count'] += 1
                turns[model]['with_tools'] += bool(d.get('tool_calls'))
                turns[model]['single_tool'] += d.get('tool_calls') == 1
                turns[model]['truncated'] += bool(d.get('truncated'))
                for key in ('input', 'output', 'cache_read', 'cache_write', 'total'):
                    usage[model][key] += (d.get('usage') or {}).get(key, 0)
                row['model_duration_ms'] += d.get('duration_ms', 0)
                row['max_context_est'] = max(row['max_context_est'], d.get('context_tokens_est', 0))
                row['model_turns_with_tools'] += bool(d.get('tool_calls'))
                row['single_tool_turns'] += d.get('tool_calls') == 1
            elif t == 'tool_result':
                name = d.get('name', 'unknown')
                tools[name] += 1
                size = len(d.get('result', '').encode())
                tool_bytes[name] += size
                row['tool_result_bytes'] += size
                row['tool_duration_ms'] += d.get('duration_ms', 0)
                if d.get('error'):
                    errors[name] += 1
                    row['tool_errors'][name] += 1
            elif t == 'job_finished':
                finished_jobs[d['id']] = e['ts']
            elif t == 'job_notified' and d['id'] in finished_jobs:
                notification_lags.append((timestamp(e['ts']) - timestamp(finished_jobs[d['id']])).total_seconds())
            elif t == 'question_asked':
                question_started = e['ts']
            elif t == 'question_answered' and question_started:
                question_waits.append((timestamp(e['ts']) - timestamp(question_started)).total_seconds())
                question_started = None
            elif t == 'session_error':
                error_kinds[d.get('kind', 'unspecified')] += 1
            elif t == 'review_submitted':
                verdicts[d.get('verdict', 'unknown')] += 1
                row['max_review_round'] = max(row['max_review_round'], d.get('round', 0))
            elif t == 'subagent_finished' and d.get('role') == 'implementer':
                row['max_implementer_round'] = max(row['max_implementer_round'], d.get('round', 0))
        if stamps:
            row['first_ts'], row['last_ts'] = stamps[0], stamps[-1]
            row['span_hours'] = (timestamp(stamps[-1]) - timestamp(stamps[0])).total_seconds() / 3600
        sessions.append(row)
    nonempty = [r for r in sessions if r['events']]
    if not nonempty:
        raise ValueError('No nonempty session logs match the selected directory/cutoff')
    return dict(
        before_exclusive=before, snapshot_sha256=digest.hexdigest(), logs=len(sessions), nonempty_logs=len(nonempty),
        source_bytes=sum(r['bytes'] for r in sessions),
        invalid_lines=sum(r['invalid_lines'] for r in sessions),
        first_ts=min((r['first_ts'] for r in nonempty), key=timestamp),
        last_ts=max((r['last_ts'] for r in nonempty), key=timestamp),
        events=events, session_started_modes=modes, tool_results=tools, tool_errors=errors,
        tool_result_bytes=tool_bytes, review_verdicts=verdicts, session_error_kinds=error_kinds,
        raw_usage_by_model=usage, turns_by_model=turns,
        auto_notification_lag_seconds=dict(count=len(notification_lags),
                                          percentiles=quantiles(notification_lags),
                                          over_30_minutes=sum(v > 1800 for v in notification_lags)),
        question_wait_seconds=dict(count=len(question_waits), aggregate=sum(question_waits),
                                   percentiles=quantiles(question_waits)),
        sessions_with_event={t: sum(bool(r['events'][t]) for r in sessions) for t in events},
        percentiles={k: quantiles([r[k] for r in nonempty]) for k in
                     ('span_hours', 'max_context_est', 'model_duration_ms', 'tool_result_bytes',
                      'max_implementer_round', 'max_review_round')},
        sessions=sessions,
        caveats=[
            'Raw provider usage.total is preserved, not used as a cross-provider denominator.',
            'Model and tool duration sums overlap across actors and are not wall-clock time.',
            'Context values are recorded estimates; neither model limits nor exact API input.',
            'tool_result.error excludes most ordinary nonzero Bash exit codes.',
            'Logs may contain more than one session_started or idle event.',
            'Live files are snapshotted separately, not transactionally as a corpus.',
        ])


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('sessions', type=Path)
    parser.add_argument('--before', help='Exclusive UTC event cutoff, e.g. 2026-09-17T00:00:00Z; excludes empty logs')
    parser.add_argument('--summary', action='store_true', help='Omit per-log rows')
    args = parser.parse_args()
    result = census(args.sessions, args.before)
    if args.summary:
        result.pop('sessions')
    print(json.dumps(result, indent=2, sort_keys=True))
