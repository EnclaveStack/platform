import { describe, expect, it } from 'vitest';
import { describeSchedule, nextRuns, parseCron } from './schedule';

// The same syntax pkg/api accepts (robfig/cron standard parser with the four descriptors).

describe('schedule', () => {
  it.each(['* * * * *', '0 3 * * *', '*/5 * * * *', '0 9 * * 1-5', '30 2 1 * *', '0 0 * * 7', '15,45 8-18/2 * 1,6 *', '@hourly', '@daily', '@weekly', '@monthly'])(
    'accepts %s', (e) => expect(() => parseCron(e)).not.toThrow());
  it.each(['', '* * * *', '61 * * * *', '* 24 * * *', '* * 0 * *', '* * * 13 *', '*/0 * * * *', '@every 5m', '@yearly', '5-1 * * * *'])(
    'rejects %j', (e) => expect(() => parseCron(e)).toThrow());

  it('describes common schedules in words', () => {
    expect(describeSchedule('* * * * *')).toBe('Every minute');
    expect(describeSchedule('*/15 * * * *')).toBe('Every 15 minutes');
    expect(describeSchedule('5 * * * *')).toBe('Every hour at :05');
    expect(describeSchedule('0 3 * * *', 'Asia/Kolkata')).toBe('Every day at 03:00, Asia/Kolkata');
    expect(describeSchedule('@daily')).toBe('Every day at 00:00');
    expect(describeSchedule('30 9 * * 1')).toBe('Every Monday at 09:30');
    expect(describeSchedule('0 9 * * 1-5')).toBe('Every weekday at 09:00');
    expect(describeSchedule('0 2 1 * *')).toBe('On day 1 of every month at 02:00');
    expect(describeSchedule('0 8-18/2 * * *')).toBe('Cron 0 8-18/2 * * *');
    expect(describeSchedule('not a schedule')).toBe('not a schedule');
  });

  it('lists the next runs in local time', () => {
    const from = new Date(2026, 9, 21, 2, 59, 30);
    expect(nextRuns('0 3 * * *', from, 2).map((d) => [d.getDate(), d.getHours(), d.getMinutes()])).toEqual([[21, 3, 0], [22, 3, 0]]);
    expect(nextRuns('*/20 * * * *', from, 3).map((d) => d.getMinutes())).toEqual([0, 20, 40]);
    // Both day-of-month and weekday restricted: either matches (cron's OR rule). 2026-10-21 is a Wednesday.
    const either = nextRuns('0 0 25 * 5', from, 2).map((d) => d.getDate());
    expect(either).toEqual([23, 25]);
  });
});
