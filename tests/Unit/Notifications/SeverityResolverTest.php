<?php

declare(strict_types=1);

namespace Tests\Unit\Notifications;

use App\Services\Notifications\SeverityResolver;
use PHPUnit\Framework\TestCase;

class SeverityResolverTest extends TestCase
{
    private SeverityResolver $resolver;

    protected function setUp(): void
    {
        parent::setUp();
        $this->resolver = new SeverityResolver;
    }

    /** @return array<string, array{string, string}> */
    public static function provideTypes(): array
    {
        return [
            'backup failure -> critical' => ['backup_failure', 'critical'],
            'agent error      -> critical' => ['agent_error', 'critical'],
            'service crash    -> critical' => ['service_crash', 'critical'],
            'intrusion        -> critical' => ['intrusion_detected', 'critical'],
            'ssl expiring     -> warning' => ['ssl_expiring', 'warning'],
            'ssl_errors       -> critical (errors keyword wins)' => ['ssl_errors', 'critical'],
            'quota warning    -> warning' => ['quota_warning', 'warning'],
            'deprecated api   -> warning' => ['deprecated_api_usage', 'warning'],
            'ssh login (plain) -> info' => ['ssh_logins', 'info'],
            'system update    -> info' => ['system_updates', 'info'],
            'test             -> info' => ['test', 'info'],
        ];
    }

    /**
     * @dataProvider provideTypes
     */
    public function test_resolves_type_to_severity(string $type, string $expected): void
    {
        $this->assertSame($expected, $this->resolver->resolve($type));
    }

    public function test_is_case_insensitive(): void
    {
        $this->assertSame('critical', $this->resolver->resolve('Backup_FAILURE'));
    }
}
