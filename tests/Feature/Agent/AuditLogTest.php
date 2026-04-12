<?php

declare(strict_types=1);

namespace Tests\Feature\Agent;

use App\Agent\AuditLog;
use PHPUnit\Framework\TestCase;

class AuditLogTest extends TestCase
{
    private string $logPath;

    protected function setUp(): void
    {
        parent::setUp();
        $this->logPath = sys_get_temp_dir().'/jabali-agent-audit-test-'.uniqid().'.log';
    }

    protected function tearDown(): void
    {
        if (file_exists($this->logPath)) {
            unlink($this->logPath);
        }
        parent::tearDown();
    }

    public function test_appends_entry_as_json_line(): void
    {
        $audit = new AuditLog($this->logPath);
        $audit->append(
            method: 'user.create',
            argsSummary: ['username' => 'alice'],
            callerUid: 33,
            result: 'ok',
            durationMs: 42,
        );

        $contents = file_get_contents($this->logPath);
        $line = trim($contents);
        $data = json_decode($line, true, flags: JSON_THROW_ON_ERROR);

        $this->assertEquals('user.create', $data['method']);
        $this->assertEquals(33, $data['caller_uid']);
        $this->assertEquals('ok', $data['result']);
        $this->assertEquals(42, $data['duration_ms']);
        $this->assertArrayHasKey('ts', $data);
        $this->assertArrayHasKey('args_hash', $data);
    }

    public function test_args_are_hashed_not_logged_raw(): void
    {
        $audit = new AuditLog($this->logPath);
        $audit->append(
            method: 'user.setPassword',
            argsSummary: ['username' => 'alice', 'password' => 'super-secret-123'],
            callerUid: 33,
            result: 'ok',
            durationMs: 15,
        );

        $contents = file_get_contents($this->logPath);
        $this->assertStringNotContainsString('super-secret-123', $contents, 'raw args must not appear in audit log');
        $this->assertStringNotContainsString('alice', $contents, 'even usernames are redacted behind the hash');
    }

    public function test_appends_multiple_lines_atomically(): void
    {
        $audit = new AuditLog($this->logPath);

        for ($i = 0; $i < 50; $i++) {
            $audit->append('method.'.$i, [], 33, 'ok', $i);
        }

        $lines = file($this->logPath, FILE_IGNORE_NEW_LINES | FILE_SKIP_EMPTY_LINES);
        $this->assertCount(50, $lines);

        foreach ($lines as $line) {
            $data = json_decode($line, true, flags: JSON_THROW_ON_ERROR);
            $this->assertIsArray($data);
            $this->assertArrayHasKey('method', $data);
        }
    }

    public function test_creates_file_with_restrictive_perms(): void
    {
        $audit = new AuditLog($this->logPath);
        $audit->append('test', [], 0, 'ok', 1);

        $perms = fileperms($this->logPath) & 0777;
        $this->assertEquals(0600, $perms, 'audit log must be mode 0600');
    }

    public function test_records_failure_result(): void
    {
        $audit = new AuditLog($this->logPath);
        $audit->append(
            method: 'ssl.issue',
            argsSummary: ['domain' => 'example.com'],
            callerUid: 33,
            result: 'error',
            durationMs: 500,
            error: 'certbot: rate limit exceeded',
        );

        $contents = trim(file_get_contents($this->logPath));
        $data = json_decode($contents, true, flags: JSON_THROW_ON_ERROR);

        $this->assertEquals('error', $data['result']);
        $this->assertEquals('certbot: rate limit exceeded', $data['error']);
    }

    public function test_error_truncated_at_512_chars(): void
    {
        $long = str_repeat('X', 1000);
        $audit = new AuditLog($this->logPath);
        $audit->append('x', [], 0, 'error', 1, $long);

        $contents = trim(file_get_contents($this->logPath));
        $data = json_decode($contents, true, flags: JSON_THROW_ON_ERROR);

        $this->assertLessThanOrEqual(512, strlen($data['error']));
    }
}
