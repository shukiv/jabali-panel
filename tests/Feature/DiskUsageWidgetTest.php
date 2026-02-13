<?php

declare(strict_types=1);

namespace Tests\Feature;

use App\Filament\Jabali\Widgets\DiskUsageWidget;
use App\Models\User;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Livewire\Livewire;
use Tests\TestCase;

class DiskUsageWidgetTest extends TestCase
{
    use RefreshDatabase;

    public function test_chart_is_hidden_when_quota_is_unlimited(): void
    {
        $user = User::factory()->create([
            'disk_quota_mb' => 0,
            'home_directory' => '/nonexistent',
        ]);

        $this->actingAs($user);

        Livewire::test(DiskUsageWidget::class)
            ->assertDontSee('x-ref="chart"', false);
    }

    public function test_chart_is_visible_when_quota_is_limited(): void
    {
        $user = User::factory()->create([
            'disk_quota_mb' => 100,
            'home_directory' => '/nonexistent',
        ]);

        $this->actingAs($user);

        Livewire::test(DiskUsageWidget::class)
            ->assertSee('x-ref="chart"', false);
    }
}
