<?php

declare(strict_types=1);

namespace Tests\Feature\Filament;

use App\Filament\Admin\Pages\DatabaseTuning;
use App\Models\User;
use App\Services\Agent\AgentClient;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Livewire\Livewire;
use Tests\TestCase;

class TestableDatabaseTuning extends DatabaseTuning
{
    protected function getAgent(): AgentClient
    {
        return new class extends AgentClient
        {
            public function send(string $action, array $params = []): array
            {
                if ($action === 'database.get_variables') {
                    return [
                        'success' => true,
                        'variables' => [
                            ['name' => 'max_connections', 'value' => '200'],
                        ],
                    ];
                }

                if ($action === 'database.set_global') {
                    return ['success' => true];
                }

                if ($action === 'database.persist_tuning') {
                    return ['success' => true];
                }

                return ['success' => false];
            }
        };
    }
}

class DatabaseTuningPageTest extends TestCase
{
    use RefreshDatabase;

    public function test_database_tuning_loads_variables_from_agent(): void
    {
        $admin = User::factory()->admin()->create();

        $this->actingAs($admin);

        Livewire::test(TestableDatabaseTuning::class)
            ->call('loadVariables')
            ->assertSet('variables.0.name', 'max_connections')
            ->assertSet('variables.0.value', '200');
    }
}
