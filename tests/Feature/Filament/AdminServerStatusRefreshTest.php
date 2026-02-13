<?php

declare(strict_types=1);

namespace Tests\Feature\Filament;

use App\Filament\Admin\Pages\ServerStatus;
use App\Models\ServerProcess;
use App\Services\Agent\AgentClient;
use Illuminate\Foundation\Testing\RefreshDatabase;
use ReflectionProperty;
use Tests\TestCase;

class AdminServerStatusRefreshTest extends TestCase
{
    use RefreshDatabase;

    public function test_load_metrics_flushes_cached_table_records(): void
    {
        $page = app(ServerStatus::class);
        $agent = new class extends AgentClient
        {
            private int $calls = 0;

            public function metricsOverview(): array
            {
                return [];
            }

            public function metricsDisk(): array
            {
                return ['data' => []];
            }

            public function metricsNetwork(): array
            {
                return ['data' => []];
            }

            public function metricsProcesses(int $limit = 15, string $sortBy = 'cpu'): array
            {
                $this->calls++;
                $command = $this->calls === 1 ? 'first-cmd' : 'second-cmd';

                return [
                    'data' => [
                        'total' => 1,
                        'running' => 1,
                        'top' => [
                            [
                                'pid' => $this->calls,
                                'user' => 'root',
                                'command' => $command,
                                'cpu' => 1.2,
                                'memory' => 0.3,
                            ],
                        ],
                    ],
                ];
            }
        };

        $this->setProtectedProperty($page, 'agent', $agent);

        $page->loadMetrics();

        $this->assertDatabaseHas('server_processes', [
            'command' => 'first-cmd',
        ]);

        $this->setProtectedProperty($page, 'cachedTableRecords', ServerProcess::latestBatch()->get());

        $page->loadMetrics();

        $this->assertDatabaseHas('server_processes', [
            'command' => 'second-cmd',
        ]);
        $this->assertNull($this->getProtectedProperty($page, 'cachedTableRecords'));
    }

    private function setProtectedProperty(object $object, string $property, mixed $value): void
    {
        $reflection = new ReflectionProperty($object, $property);
        $reflection->setAccessible(true);
        $reflection->setValue($object, $value);
    }

    private function getProtectedProperty(object $object, string $property): mixed
    {
        $reflection = new ReflectionProperty($object, $property);
        $reflection->setAccessible(true);

        return $reflection->getValue($object);
    }
}
