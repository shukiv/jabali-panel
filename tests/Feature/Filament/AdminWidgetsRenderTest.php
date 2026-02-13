<?php

declare(strict_types=1);

namespace Tests\Feature\Filament;

use App\Filament\Admin\Widgets\DiskUsageWidget as AdminDiskUsageWidget;
use App\Filament\Admin\Widgets\ProcessesWidget;
use App\Filament\Admin\Widgets\ServerChartsWidget;
use App\Models\User;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Livewire\Livewire;
use Tests\TestCase;

class AdminWidgetsRenderTest extends TestCase
{
    use RefreshDatabase;

    public function test_disk_usage_widget_renders(): void
    {
        $admin = User::factory()->admin()->create();

        $this->actingAs($admin);

        Livewire::test(AdminDiskUsageWidget::class)
            ->assertStatus(200)
            ->assertSee('Disk Usage');
    }

    public function test_processes_widget_renders(): void
    {
        $admin = User::factory()->admin()->create();

        $this->actingAs($admin);

        Livewire::test(ProcessesWidget::class)
            ->assertStatus(200)
            ->assertSee('Top Processes');
    }

    public function test_server_charts_widget_renders(): void
    {
        $admin = User::factory()->admin()->create();

        $this->actingAs($admin);

        Livewire::test(ServerChartsWidget::class)
            ->assertStatus(200)
            ->assertSee('Load');
    }
}
