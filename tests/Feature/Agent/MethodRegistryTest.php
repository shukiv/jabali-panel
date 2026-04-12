<?php

declare(strict_types=1);

namespace Tests\Feature\Agent;

use App\Agent\MethodRegistry;
use InvalidArgumentException;
use PHPUnit\Framework\TestCase;

class MethodRegistryTest extends TestCase
{
    public function test_accepts_registered_method_with_required_args(): void
    {
        $registry = new MethodRegistry([
            'user.create' => ['required' => ['username'], 'optional' => ['password']],
        ]);

        // Does not throw — required arg present.
        $registry->assertValid('user.create', ['username' => 'alice']);
        $this->assertTrue(true);
    }

    public function test_rejects_unregistered_method(): void
    {
        $registry = new MethodRegistry([
            'user.create' => ['required' => [], 'optional' => []],
        ]);

        $this->expectException(InvalidArgumentException::class);
        $this->expectExceptionMessageMatches('/unknown method/i');
        $registry->assertValid('exploded.exec', []);
    }

    public function test_rejects_missing_required_arg(): void
    {
        $registry = new MethodRegistry([
            'user.create' => ['required' => ['username'], 'optional' => []],
        ]);

        $this->expectException(InvalidArgumentException::class);
        $this->expectExceptionMessageMatches('/missing required/i');
        $registry->assertValid('user.create', []);
    }

    public function test_allows_optional_args(): void
    {
        $registry = new MethodRegistry([
            'user.create' => ['required' => ['username'], 'optional' => ['password']],
        ]);

        $registry->assertValid('user.create', ['username' => 'alice', 'password' => 'pw']);
        $this->assertTrue(true);
    }

    public function test_rejects_unexpected_args(): void
    {
        $registry = new MethodRegistry([
            'user.create' => ['required' => ['username'], 'optional' => ['password']],
        ]);

        $this->expectException(InvalidArgumentException::class);
        $this->expectExceptionMessageMatches('/unexpected/i');
        $registry->assertValid('user.create', ['username' => 'a', 'drop_table' => 'users']);
    }

    public function test_is_registered(): void
    {
        $registry = new MethodRegistry([
            'user.create' => ['required' => [], 'optional' => []],
        ]);

        $this->assertTrue($registry->isRegistered('user.create'));
        $this->assertFalse($registry->isRegistered('nope'));
    }

    public function test_default_registry_includes_job_methods(): void
    {
        $registry = MethodRegistry::default();

        $this->assertTrue($registry->isRegistered('job.start'));
        $this->assertTrue($registry->isRegistered('job.status'));
        $this->assertTrue($registry->isRegistered('job.cancel'));
        $this->assertTrue($registry->isRegistered('job.list'));
        $this->assertTrue($registry->isRegistered('job.tail'));
        $this->assertTrue($registry->isRegistered('ping'));
    }
}
