<?php

declare(strict_types=1);

namespace Tests\Feature\Agent\Jobs;

use App\Agent\Jobs\JobLog;
use PHPUnit\Framework\TestCase;

class JobLogTest extends TestCase
{
    private string $logPath;

    protected function setUp(): void
    {
        parent::setUp();
        $this->logPath = sys_get_temp_dir().'/jabali-job-log-'.uniqid().'.log';
    }

    protected function tearDown(): void
    {
        if (file_exists($this->logPath)) {
            unlink($this->logPath);
        }
        parent::tearDown();
    }

    public function test_tail_returns_last_n_events(): void
    {
        $this->writeLog([
            '{"ts":"2026-04-13T01:00:00Z","event":"started"}',
            '{"ts":"2026-04-13T01:01:00Z","event":"progress","percent":10}',
            '{"ts":"2026-04-13T01:02:00Z","event":"progress","percent":50}',
            '{"ts":"2026-04-13T01:03:00Z","event":"done"}',
        ]);

        $log = new JobLog($this->logPath);
        $events = $log->tail(2);

        $this->assertCount(2, $events);
        $this->assertEquals('progress', $events[0]->kind->value);
        $this->assertEquals(50, $events[0]->percent);
        $this->assertEquals('done', $events[1]->kind->value);
    }

    public function test_tail_missing_file_returns_empty(): void
    {
        $log = new JobLog('/nonexistent');
        $this->assertEmpty($log->tail(10));
    }

    public function test_since_returns_events_after_timestamp(): void
    {
        $this->writeLog([
            '{"ts":"2026-04-13T01:00:00Z","event":"started"}',
            '{"ts":"2026-04-13T01:01:00Z","event":"progress","percent":10}',
            '{"ts":"2026-04-13T01:02:00Z","event":"progress","percent":50}',
        ]);

        $log = new JobLog($this->logPath);
        $events = $log->since(new \DateTimeImmutable('2026-04-13T01:00:30Z'));

        $this->assertCount(2, $events);
        $this->assertEquals(10, $events[0]->percent);
    }

    public function test_latest_status(): void
    {
        $this->writeLog([
            '{"ts":"2026-04-13T01:00:00Z","event":"started"}',
            '{"ts":"2026-04-13T01:01:00Z","event":"progress","percent":50,"step":"uploading"}',
        ]);

        $log = new JobLog($this->logPath);
        $status = $log->latestStatus();

        $this->assertEquals(50, $status['progress']);
        $this->assertEquals('uploading', $status['step']);
    }

    public function test_skips_malformed_lines(): void
    {
        $this->writeLog([
            '{"ts":"2026-04-13T01:00:00Z","event":"started"}',
            'not valid json',
            '{"ts":"2026-04-13T01:01:00Z","event":"done"}',
        ]);

        $log = new JobLog($this->logPath);
        $events = $log->tail(10);

        $this->assertCount(2, $events); // malformed line silently skipped
    }

    private function writeLog(array $lines): void
    {
        file_put_contents($this->logPath, implode("\n", $lines)."\n");
    }
}
