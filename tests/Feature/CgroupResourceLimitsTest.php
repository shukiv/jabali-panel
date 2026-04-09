<?php

declare(strict_types=1);

namespace Tests\Feature;

use App\Models\HostingPackage;
use App\Models\User;
use App\Services\Agent\AgentClient;
use App\Services\UserDeletionService;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Mockery;
use Tests\TestCase;

class CgroupResourceLimitsTest extends TestCase
{
    use RefreshDatabase;

    public function test_user_inherits_resource_limits_from_package(): void
    {
        $package = HostingPackage::create([
            'name' => 'Starter',
            'cpu_quota' => 100,
            'memory_limit_mb' => 512,
            'io_read_mbps' => 50,
            'io_write_mbps' => 25,
            'max_processes' => 50,
        ]);

        $user = User::factory()->create([
            'hosting_package_id' => $package->id,
        ]);

        $limits = $user->getEffectiveResourceLimits();

        $this->assertSame(100, $limits['cpu_quota']);
        $this->assertSame(512, $limits['memory_limit_mb']);
        $this->assertSame(50, $limits['io_read_mbps']);
        $this->assertSame(25, $limits['io_write_mbps']);
        $this->assertSame(50, $limits['max_processes']);
    }

    public function test_user_override_takes_precedence_over_package(): void
    {
        $package = HostingPackage::create([
            'name' => 'Standard',
            'cpu_quota' => 100,
            'memory_limit_mb' => 512,
            'max_processes' => 50,
        ]);

        $user = User::factory()->create([
            'hosting_package_id' => $package->id,
            'cpu_quota' => 200,
            'memory_limit_mb' => 1024,
        ]);

        $limits = $user->getEffectiveResourceLimits();

        $this->assertSame(200, $limits['cpu_quota']);
        $this->assertSame(1024, $limits['memory_limit_mb']);
        $this->assertSame(50, $limits['max_processes']);
    }

    public function test_user_without_package_uses_own_limits(): void
    {
        $user = User::factory()->create([
            'hosting_package_id' => null,
            'cpu_quota' => 150,
            'max_processes' => 100,
        ]);

        $limits = $user->getEffectiveResourceLimits();

        $this->assertSame(150, $limits['cpu_quota']);
        $this->assertSame(100, $limits['max_processes']);
        $this->assertArrayNotHasKey('memory_limit_mb', $limits);
        $this->assertArrayNotHasKey('io_read_mbps', $limits);
        $this->assertArrayNotHasKey('io_write_mbps', $limits);
    }

    public function test_user_with_no_limits_returns_empty_array(): void
    {
        $user = User::factory()->create([
            'hosting_package_id' => null,
        ]);

        $limits = $user->getEffectiveResourceLimits();

        $this->assertEmpty($limits);
    }

    public function test_partial_override_merges_with_package(): void
    {
        $package = HostingPackage::create([
            'name' => 'Pro',
            'cpu_quota' => 200,
            'memory_limit_mb' => 1024,
            'io_read_mbps' => 100,
            'io_write_mbps' => 50,
            'max_processes' => 200,
        ]);

        $user = User::factory()->create([
            'hosting_package_id' => $package->id,
            'memory_limit_mb' => 2048,
            // All other fields null — inherit from package
        ]);

        $limits = $user->getEffectiveResourceLimits();

        $this->assertSame(200, $limits['cpu_quota']);       // from package
        $this->assertSame(2048, $limits['memory_limit_mb']); // user override
        $this->assertSame(100, $limits['io_read_mbps']);     // from package
        $this->assertSame(50, $limits['io_write_mbps']);     // from package
        $this->assertSame(200, $limits['max_processes']);     // from package
    }

    public function test_package_with_no_limits_and_user_with_no_limits(): void
    {
        $package = HostingPackage::create([
            'name' => 'Free',
        ]);

        $user = User::factory()->create([
            'hosting_package_id' => $package->id,
        ]);

        $limits = $user->getEffectiveResourceLimits();

        $this->assertEmpty($limits);
    }

    public function test_deletion_calls_cgroup_remove(): void
    {
        $user = User::factory()->create([
            'username' => 'cgroupuser',
            'cpu_quota' => 100,
        ]);

        $agent = Mockery::mock(AgentClient::class);
        $agent->shouldReceive('send')->zeroOrMoreTimes()->andReturn([]);
        $agent->shouldReceive('cgroupRemove')
            ->once()
            ->with('cgroupuser')
            ->andReturn(['success' => true]);

        $service = new UserDeletionService($agent);
        $service->delete($user);

        $this->assertDatabaseMissing('users', ['id' => $user->id]);
    }

    public function test_deletion_handles_cgroup_remove_failure_gracefully(): void
    {
        $user = User::factory()->create([
            'username' => 'failuser',
        ]);

        $agent = Mockery::mock(AgentClient::class);
        $agent->shouldReceive('send')->zeroOrMoreTimes()->andReturn([]);
        $agent->shouldReceive('cgroupRemove')
            ->once()
            ->andThrow(new \RuntimeException('Agent unreachable'));

        $service = new UserDeletionService($agent);
        $service->delete($user);

        // User should still be deleted despite cgroup failure
        $this->assertDatabaseMissing('users', ['id' => $user->id]);
    }

    public function test_hosting_package_resource_fields_are_fillable(): void
    {
        $package = HostingPackage::create([
            'name' => 'Fillable Test',
            'cpu_quota' => 400,
            'memory_limit_mb' => 2048,
            'io_read_mbps' => 200,
            'io_write_mbps' => 100,
            'max_processes' => 500,
        ]);

        $this->assertDatabaseHas('hosting_packages', [
            'name' => 'Fillable Test',
            'cpu_quota' => 400,
            'memory_limit_mb' => 2048,
            'io_read_mbps' => 200,
            'io_write_mbps' => 100,
            'max_processes' => 500,
        ]);
    }

    public function test_user_resource_fields_are_fillable(): void
    {
        $user = User::factory()->create([
            'cpu_quota' => 150,
            'memory_limit_mb' => 768,
            'io_read_mbps' => 75,
            'io_write_mbps' => 30,
            'max_processes' => 80,
        ]);

        $this->assertDatabaseHas('users', [
            'id' => $user->id,
            'cpu_quota' => 150,
            'memory_limit_mb' => 768,
            'io_read_mbps' => 75,
            'io_write_mbps' => 30,
            'max_processes' => 80,
        ]);
    }

    public function test_resource_fields_are_nullable(): void
    {
        $package = HostingPackage::create([
            'name' => 'Nullable Test',
            'cpu_quota' => null,
            'memory_limit_mb' => null,
        ]);

        $this->assertDatabaseHas('hosting_packages', [
            'name' => 'Nullable Test',
            'cpu_quota' => null,
            'memory_limit_mb' => null,
        ]);
    }
}
