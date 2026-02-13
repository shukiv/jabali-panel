<?php

declare(strict_types=1);

namespace Tests\Unit;

use App\Models\BackupSchedule;
use Carbon\Carbon;
use Tests\TestCase;

class BackupScheduleNextRunTest extends TestCase
{
    public function test_calculate_next_run_uses_system_timezone_and_stores_utc(): void
    {
        $timezone = trim((string) @file_get_contents('/etc/timezone'));
        if ($timezone === '') {
            $timezone = trim((string) @shell_exec('timedatectl show -p Timezone --value 2>/dev/null'));
        }
        if ($timezone === '') {
            $timezone = 'UTC';
        }
        $now = Carbon::create(2026, 2, 2, 1, 0, 0, $timezone);
        Carbon::setTestNow($now);

        $schedule = new BackupSchedule([
            'frequency' => 'daily',
            'time' => '02:00',
            'is_active' => true,
        ]);

        $nextRun = $schedule->calculateNextRun();

        $expectedLocal = $now->copy()->setTime(2, 0, 0);
        if ($expectedLocal->isPast()) {
            $expectedLocal->addDay();
        }

        $expectedUtc = $expectedLocal->copy()->setTimezone('UTC');

        $this->assertSame($expectedUtc->timestamp, $nextRun->timestamp);
    }
}
