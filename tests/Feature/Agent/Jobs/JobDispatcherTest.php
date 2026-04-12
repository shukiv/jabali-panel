<?php

declare(strict_types=1);

namespace Tests\Feature\Agent\Jobs;

use App\Agent\Jobs\JobDispatcher;
use App\Agent\Jobs\JobStore;
use App\Agent\Jobs\SystemdRunnerInterface;
use PHPUnit\Framework\TestCase;
use RuntimeException;

class JobDispatcherTest extends TestCase
{
    private string $dbPath;

    private string $logDir;

    protected function setUp(): void
    {
        parent::setUp();
        $this->dbPath = sys_get_temp_dir().'/jabali-dispatcher-'.uniqid().'.db';
        $this->logDir = sys_get_temp_dir().'/jabali-dispatcher-logs-'.uniqid();
        mkdir($this->logDir, 0755, recursive: true);
    }

    protected function tearDown(): void
    {
        foreach (glob($this->logDir.'/*') as $f) {
            unlink($f);
        }
        @rmdir($this->logDir);
        foreach ([$this->dbPath, $this->dbPath.'-wal', $this->dbPath.'-shm'] as $p) {
            if (file_exists($p)) {
                unlink($p);
            }
        }
        parent::tearDown();
    }

    public function test_start_creates_row_and_invokes_systemd_run(): void
    {
        $runner = new FakeSystemdRunner;
        $runner->startResult = ['unit' => 'jabali-job-test', 'pid' => 12345];

        $dispatcher = new JobDispatcher(
            store: new JobStore($this->dbPath),
            runner: $runner,
            logDir: $this->logDir,
        );

        $result = $dispatcher->start(
            type: 'backup',
            argv: ['/opt/jabali-backup/bin/run'],
            limits: ['cpu' => 50, 'memory' => '1G', 'io' => 100],
            payload: ['target' => 'user1'],
        );

        $this->assertArrayHasKey('id', $result);
        $this->assertArrayHasKey('unit', $result);
        $this->assertArrayHasKey('pid', $result);
        $this->assertArrayHasKey('log_path', $result);
        $this->assertStringContainsString($result['id'], $result['log_path']);

        // systemd-run was called with expected flags
        $this->assertNotNull($runner->lastStartArgs);
        $this->assertEquals('backup', $runner->lastStartArgs['type']);
        $this->assertEquals(50, $runner->lastStartArgs['limits']['cpu']);
        $this->assertContains('--task-id='.$result['id'], $runner->lastStartArgs['argv']);
        $this->assertContains('--progress-log='.$result['log_path'], $runner->lastStartArgs['argv']);
    }

    public function test_start_rejects_relative_binary(): void
    {
        $runner = new FakeSystemdRunner;
        $dispatcher = new JobDispatcher(
            store: new JobStore($this->dbPath),
            runner: $runner,
            logDir: $this->logDir,
        );

        $this->expectException(\InvalidArgumentException::class);
        $dispatcher->start(type: 'x', argv: ['relative-binary'], limits: [], payload: []);
    }

    public function test_status_returns_row_and_recent_events(): void
    {
        $runner = new FakeSystemdRunner;
        $runner->startResult = ['unit' => 'jabali-job-x', 'pid' => 1];

        $dispatcher = new JobDispatcher(
            store: new JobStore($this->dbPath),
            runner: $runner,
            logDir: $this->logDir,
        );

        $started = $dispatcher->start(type: 'backup', argv: ['/bin/true'], limits: [], payload: []);
        $id = $started['id'];

        // Simulate the spawned binary writing events to the log
        file_put_contents($started['log_path'], implode("\n", [
            '{"ts":"2026-04-13T01:00:00Z","event":"started"}',
            '{"ts":"2026-04-13T01:01:00Z","event":"progress","percent":42,"step":"uploading"}',
        ])."\n");

        $status = $dispatcher->status($id);

        $this->assertEquals($id, $status['id']);
        $this->assertIsArray($status['recent_events']);
        $this->assertCount(2, $status['recent_events']);
        $this->assertEquals(42, $status['recent_events'][1]['percent']);
    }

    public function test_status_unknown_id_throws(): void
    {
        $dispatcher = new JobDispatcher(
            store: new JobStore($this->dbPath),
            runner: new FakeSystemdRunner,
            logDir: $this->logDir,
        );

        $this->expectException(RuntimeException::class);
        $dispatcher->status('nonexistent');
    }

    public function test_cancel_invokes_systemctl_stop(): void
    {
        $runner = new FakeSystemdRunner;
        $runner->startResult = ['unit' => 'jabali-job-cancel', 'pid' => 99];

        $dispatcher = new JobDispatcher(
            store: new JobStore($this->dbPath),
            runner: $runner,
            logDir: $this->logDir,
        );

        $started = $dispatcher->start(type: 'backup', argv: ['/bin/true'], limits: [], payload: []);
        $dispatcher->cancel($started['id']);

        $this->assertEquals('jabali-job-cancel', $runner->lastCancelUnit);

        $status = $dispatcher->status($started['id']);
        $this->assertEquals('canceled', $status['status']);
    }

    public function test_list_returns_filtered_rows(): void
    {
        $runner = new FakeSystemdRunner;
        $runner->startResult = ['unit' => 'u', 'pid' => 1];

        $dispatcher = new JobDispatcher(
            store: new JobStore($this->dbPath),
            runner: $runner,
            logDir: $this->logDir,
        );

        $a = $dispatcher->start(type: 'backup', argv: ['/bin/true'], limits: [], payload: []);
        $b = $dispatcher->start(type: 'ssl_issue', argv: ['/bin/true'], limits: [], payload: []);

        $all = $dispatcher->list([]);
        $this->assertCount(2, $all);

        $backups = $dispatcher->list(['type' => 'backup']);
        $this->assertCount(1, $backups);
        $this->assertEquals($a['id'], $backups[0]['id']);
    }

    public function test_tail_returns_events_since_timestamp(): void
    {
        $runner = new FakeSystemdRunner;
        $runner->startResult = ['unit' => 'u', 'pid' => 1];

        $dispatcher = new JobDispatcher(
            store: new JobStore($this->dbPath),
            runner: $runner,
            logDir: $this->logDir,
        );

        $started = $dispatcher->start(type: 'backup', argv: ['/bin/true'], limits: [], payload: []);

        file_put_contents($started['log_path'], implode("\n", [
            '{"ts":"2026-04-13T01:00:00Z","event":"started"}',
            '{"ts":"2026-04-13T01:01:00Z","event":"progress","percent":50}',
            '{"ts":"2026-04-13T01:02:00Z","event":"done"}',
        ])."\n");

        $events = $dispatcher->tail($started['id'], since: '2026-04-13T01:00:30Z');
        $this->assertCount(2, $events);
    }
}

class FakeSystemdRunner implements SystemdRunnerInterface
{
    public ?array $startResult = null;

    public ?array $lastStartArgs = null;

    public ?string $lastCancelUnit = null;

    public function startTransient(string $type, string $unit, array $argv, array $limits): array
    {
        $this->lastStartArgs = ['type' => $type, 'unit' => $unit, 'argv' => $argv, 'limits' => $limits];

        return $this->startResult ?? ['unit' => $unit, 'pid' => 1];
    }

    public function stopUnit(string $unit, int $graceSec = 10): void
    {
        $this->lastCancelUnit = $unit;
    }

    public function isActive(string $unit): bool
    {
        return true;
    }
}
