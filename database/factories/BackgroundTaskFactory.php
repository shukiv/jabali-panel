<?php

declare(strict_types=1);

namespace Database\Factories;

use App\Enums\BackgroundTaskStatus;
use App\Enums\BackgroundTaskType;
use App\Models\BackgroundTask;
use Illuminate\Database\Eloquent\Factories\Factory;

/**
 * @extends Factory<BackgroundTask>
 */
class BackgroundTaskFactory extends Factory
{
    protected $model = BackgroundTask::class;

    public function definition(): array
    {
        $type = fake()->randomElement(BackgroundTaskType::cases());

        return [
            'type' => $type,
            'status' => BackgroundTaskStatus::Pending,
            'payload' => [],
            'max_retries' => $type->defaultMaxRetries(),
        ];
    }

    public function pending(): static
    {
        return $this->state(fn () => ['status' => BackgroundTaskStatus::Pending]);
    }

    public function running(): static
    {
        return $this->state(fn () => [
            'status' => BackgroundTaskStatus::Running,
            'pid' => fake()->numberBetween(1000, 99999),
            'systemd_unit' => 'jabali-job-'.fake()->uuid(),
            'started_at' => now(),
        ]);
    }

    public function done(): static
    {
        return $this->state(fn () => [
            'status' => BackgroundTaskStatus::Done,
            'progress' => 100,
            'exit_code' => 0,
            'started_at' => now()->subMinutes(5),
            'finished_at' => now(),
        ]);
    }

    public function failed(): static
    {
        return $this->state(fn () => [
            'status' => BackgroundTaskStatus::Failed,
            'exit_code' => 1,
            'error_message' => 'Simulated failure',
            'started_at' => now()->subMinutes(2),
            'finished_at' => now(),
        ]);
    }
}
