<?php

declare(strict_types=1);

namespace Tests\Feature\BackgroundTasks;

use App\Enums\BackgroundTaskType;
use App\Models\BackgroundTask;
use App\Services\Agent\AgentClientInterface;
use App\Services\BackgroundTasks\AddonInstaller;
use Mockery;
use Tests\TestCase;

class AddonInstallMigrationTest extends TestCase
{
    protected function tearDown(): void
    {
        Mockery::close();
        BackgroundTask::query()->delete();
        parent::tearDown();
    }

    public function test_install_via_agent_v2_dispatches_task(): void
    {
        config(['jabali.agent_v2.addons_enabled' => true]);
        config([
            'jabali-addons' => [
                'testaddon' => [
                    'name' => 'Test Addon',
                    'binary' => '/nonexistent/binary',
                    'install_url' => 'https://example.com/install.sh',
                    'uninstall_command' => '/nonexistent/uninstall.sh',
                ],
            ],
        ]);

        $agent = Mockery::mock(AgentClientInterface::class);
        $agent->shouldReceive('send')
            ->once()
            ->with('job.start', Mockery::on(function ($params) {
                return $params['type'] === BackgroundTaskType::AddonInstall->value
                    && str_contains($params['argv'][2] ?? '', 'jabali:tasks:run-addon-install')
                    && $params['payload']['addon'] === 'testaddon';
            }))
            ->andReturn(['success' => true, 'unit' => 'u', 'pid' => 1, 'started_at' => '2026-04-13T01:00:00Z']);
        $this->app->instance(AgentClientInterface::class, $agent);

        $installer = app(AddonInstaller::class);
        $task = $installer->install('testaddon');

        $this->assertNotNull($task);
        $this->assertEquals(BackgroundTaskType::AddonInstall, $task->type);
        $this->assertEquals('addon:install:testaddon', $task->dedupe_key);
    }

    public function test_uninstall_via_agent_v2_dispatches_task(): void
    {
        config(['jabali.agent_v2.addons_enabled' => true]);
        $binaryPath = sys_get_temp_dir().'/fake-addon-binary-'.uniqid();
        touch($binaryPath);

        config([
            'jabali-addons' => [
                'testaddon' => [
                    'name' => 'Test Addon',
                    'binary' => $binaryPath,
                    'install_url' => 'https://example.com/install.sh',
                    'uninstall_command' => '/bin/true',
                ],
            ],
        ]);

        $agent = Mockery::mock(AgentClientInterface::class);
        $agent->shouldReceive('send')
            ->once()
            ->with('job.start', Mockery::on(function ($params) {
                return $params['type'] === BackgroundTaskType::AddonUninstall->value;
            }))
            ->andReturn(['success' => true, 'unit' => 'u', 'pid' => 1, 'started_at' => '2026-04-13T01:00:00Z']);
        $this->app->instance(AgentClientInterface::class, $agent);

        $installer = app(AddonInstaller::class);
        $task = $installer->uninstall('testaddon');

        $this->assertNotNull($task);
        $this->assertEquals(BackgroundTaskType::AddonUninstall, $task->type);

        unlink($binaryPath);
    }

    public function test_install_rejects_unknown_addon(): void
    {
        config(['jabali.agent_v2.addons_enabled' => true]);
        config(['jabali-addons' => []]);

        $installer = app(AddonInstaller::class);

        $this->expectException(\InvalidArgumentException::class);
        $installer->install('nonexistent');
    }

    public function test_install_rejects_already_installed_addon(): void
    {
        config(['jabali.agent_v2.addons_enabled' => true]);
        $binaryPath = sys_get_temp_dir().'/already-installed-'.uniqid();
        touch($binaryPath);

        config([
            'jabali-addons' => [
                'testaddon' => [
                    'name' => 'Test Addon',
                    'binary' => $binaryPath,
                    'install_url' => 'https://example.com/install.sh',
                    'uninstall_command' => '/bin/true',
                ],
            ],
        ]);

        $installer = app(AddonInstaller::class);

        $this->expectException(\RuntimeException::class);
        $this->expectExceptionMessageMatches('/already installed/i');
        $installer->install('testaddon');

        unlink($binaryPath);
    }
}
