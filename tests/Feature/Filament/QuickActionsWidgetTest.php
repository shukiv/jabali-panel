<?php

declare(strict_types=1);

namespace Tests\Feature\Filament;

use App\Filament\Admin\Widgets\QuickActions as AdminQuickActions;
use App\Filament\Jabali\Widgets\QuickActionsWidget;
use App\Models\User;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Livewire\Livewire;
use Tests\TestCase;

class QuickActionsWidgetTest extends TestCase
{
    use RefreshDatabase;

    public function test_user_quick_actions_widget_renders(): void
    {
        $user = User::factory()->create();

        $this->actingAs($user);

        Livewire::test(QuickActionsWidget::class)
            ->assertStatus(200)
            ->assertSee('flex flex-wrap gap-2', false);
    }

    public function test_admin_quick_actions_widget_renders(): void
    {
        $admin = User::factory()->admin()->create();

        $this->actingAs($admin);

        Livewire::test(AdminQuickActions::class)
            ->assertStatus(200)
            ->assertSee('flex flex-wrap gap-2', false);
    }
}
