<?php

declare(strict_types=1);

namespace Tests\Feature;

use App\Filament\Jabali\Widgets\StatsOverview;
use App\Models\User;
use App\Services\Agent\AgentClient;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Tests\TestCase;

class StatsOverviewDatabaseCountTest extends TestCase
{
    use RefreshDatabase;

    public function test_stats_overview_uses_agent_database_list(): void
    {
        $user = User::factory()->create([
            'username' => 'laundryshco',
        ]);

        $this->actingAs($user);

        $fakeAgent = new class extends AgentClient
        {
            public function __construct() {}

            public function mysqlListDatabases(string $username): array
            {
                return [
                    'success' => true,
                    'databases' => [
                        ['name' => $username.'_wp2'],
                        ['name' => $username.'_laun_wp2'],
                    ],
                ];
            }
        };

        $this->app->instance(AgentClient::class, $fakeAgent);

        $widget = app(StatsOverview::class);
        $stats = $widget->getStats();

        $dbStat = collect($stats)->firstWhere('label', __('Databases'));

        $this->assertIsArray($dbStat);
        $this->assertSame(2, $dbStat['value']);
    }
}
