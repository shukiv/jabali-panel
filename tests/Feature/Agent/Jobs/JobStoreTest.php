<?php

declare(strict_types=1);

namespace Tests\Feature\Agent\Jobs;

use App\Agent\Jobs\JobStore;
use PHPUnit\Framework\TestCase;

class JobStoreTest extends TestCase
{
    private string $dbPath;

    protected function setUp(): void
    {
        parent::setUp();
        $this->dbPath = sys_get_temp_dir().'/jabali-agent-jobs-'.uniqid().'.db';
    }

    protected function tearDown(): void
    {
        if (file_exists($this->dbPath)) {
            unlink($this->dbPath);
        }
        if (file_exists($this->dbPath.'-wal')) {
            unlink($this->dbPath.'-wal');
        }
        if (file_exists($this->dbPath.'-shm')) {
            unlink($this->dbPath.'-shm');
        }
        parent::tearDown();
    }

    public function test_insert_and_get(): void
    {
        $store = new JobStore($this->dbPath);
        $store->insert([
            'id' => 'abc-123',
            'type' => 'backup',
            'status' => 'pending',
            'unit' => 'jabali-job-abc',
            'log_path' => '/var/lib/jabali/jobs/abc-123.log',
        ]);

        $row = $store->get('abc-123');
        $this->assertNotNull($row);
        $this->assertEquals('abc-123', $row['id']);
        $this->assertEquals('backup', $row['type']);
        $this->assertEquals('pending', $row['status']);
    }

    public function test_get_missing_returns_null(): void
    {
        $store = new JobStore($this->dbPath);
        $this->assertNull($store->get('missing-id'));
    }

    public function test_update_status(): void
    {
        $store = new JobStore($this->dbPath);
        $store->insert(['id' => 'abc', 'type' => 'backup', 'status' => 'pending', 'unit' => 'u', 'log_path' => '/l']);

        $store->updateStatus('abc', 'running', ['pid' => 1234, 'started_at' => '2026-04-13T00:00:00Z']);

        $row = $store->get('abc');
        $this->assertEquals('running', $row['status']);
        $this->assertEquals(1234, $row['pid']);
        $this->assertEquals('2026-04-13T00:00:00Z', $row['started_at']);
    }

    public function test_list_filters(): void
    {
        $store = new JobStore($this->dbPath);
        $store->insert(['id' => 'a1', 'type' => 'backup', 'status' => 'running', 'unit' => 'ua', 'log_path' => '/l']);
        $store->insert(['id' => 'a2', 'type' => 'ssl_issue', 'status' => 'running', 'unit' => 'ub', 'log_path' => '/l']);
        $store->insert(['id' => 'a3', 'type' => 'backup', 'status' => 'done', 'unit' => 'uc', 'log_path' => '/l']);

        $running = $store->list(['status' => 'running']);
        $this->assertCount(2, $running);

        $backups = $store->list(['type' => 'backup']);
        $this->assertCount(2, $backups);

        $runningBackups = $store->list(['status' => 'running', 'type' => 'backup']);
        $this->assertCount(1, $runningBackups);
        $this->assertEquals('a1', $runningBackups[0]['id']);

        $limited = $store->list(['limit' => 2]);
        $this->assertCount(2, $limited);
    }

    public function test_list_orders_by_created_desc(): void
    {
        $store = new JobStore($this->dbPath);
        $store->insert(['id' => 'old', 'type' => 't', 'status' => 's', 'unit' => 'u', 'log_path' => '/l']);
        usleep(10_000);
        $store->insert(['id' => 'new', 'type' => 't', 'status' => 's', 'unit' => 'u', 'log_path' => '/l']);

        $rows = $store->list([]);
        $this->assertEquals('new', $rows[0]['id']);
        $this->assertEquals('old', $rows[1]['id']);
    }

    public function test_creates_schema_on_first_use(): void
    {
        $store = new JobStore($this->dbPath);
        $this->assertFileExists($this->dbPath);

        // idempotent — no error if called twice
        $store2 = new JobStore($this->dbPath);
        $store2->insert(['id' => 'x', 'type' => 't', 'status' => 's', 'unit' => 'u', 'log_path' => '/l']);
        $this->assertNotNull($store2->get('x'));
    }

    public function test_wal_mode_enabled(): void
    {
        $store = new JobStore($this->dbPath);
        $store->insert(['id' => 'x', 'type' => 't', 'status' => 's', 'unit' => 'u', 'log_path' => '/l']);

        $pdo = new \PDO("sqlite:{$this->dbPath}");
        $mode = $pdo->query('PRAGMA journal_mode')->fetchColumn();
        $this->assertEqualsIgnoringCase('wal', $mode);
    }
}
