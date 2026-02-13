<?php

declare(strict_types=1);

namespace Tests\Feature\Filament;

use App\Filament\Jabali\Pages\Logs;
use App\Filament\Jabali\Pages\SshKeys;
use App\Filament\Jabali\Pages\WordPress;
use App\Models\User;
use App\Services\Agent\AgentClient;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Livewire\Livewire;
use Tests\TestCase;

class TestableLogs extends Logs
{
    protected function getAgent(): AgentClient
    {
        return new class extends AgentClient
        {
            public function send(string $action, array $params = []): array
            {
                if ($action === 'domain.list') {
                    return [
                        'success' => true,
                        'domains' => [
                            ['domain' => 'example.test'],
                        ],
                    ];
                }

                if ($action === 'logs.tail') {
                    return [
                        'success' => true,
                        'content' => 'log',
                        'file_size' => 10,
                        'lines' => 1,
                    ];
                }

                return ['success' => false];
            }
        };
    }
}

class UserPagesRenderTest extends TestCase
{
    use RefreshDatabase;

    public function test_wordpress_page_renders_scan_grid(): void
    {
        $user = User::factory()->create([
            'username' => 'testuser',
        ]);

        $this->actingAs($user);

        Livewire::test(WordPress::class)
            ->assertStatus(200)
            ->assertSee('sm:grid-cols-2', false);
    }

    public function test_ssh_keys_page_renders_wrapping_command(): void
    {
        $user = User::factory()->create([
            'username' => 'testuser',
        ]);

        $this->actingAs($user);

        Livewire::test(SshKeys::class)
            ->assertStatus(200)
            ->assertSee('break-all', false);
    }

    public function test_logs_page_renders_wrapped_buttons(): void
    {
        $user = User::factory()->create([
            'username' => 'testuser',
        ]);

        $this->actingAs($user);

        Livewire::test(TestableLogs::class)
            ->assertStatus(200)
            ->assertSee('flex flex-wrap', false);
    }
}
