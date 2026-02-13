<?php

declare(strict_types=1);

namespace Tests\Feature\Filament;

use App\Filament\Admin\Pages\Support as AdminSupport;
use App\Filament\Jabali\Pages\Support as UserSupport;
use App\Models\User;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Livewire\Livewire;
use Tests\TestCase;

class SupportPagesTest extends TestCase
{
    use RefreshDatabase;

    public function test_admin_support_page_renders_support_links(): void
    {
        $admin = User::factory()->admin()->create();

        $this->actingAs($admin);

        Livewire::test(AdminSupport::class)
            ->assertStatus(200)
            ->assertSee('Open Documentation')
            ->assertSee('GitHub Issues')
            ->assertSee('Paid Support');
    }

    public function test_user_support_page_renders_support_links(): void
    {
        $user = User::factory()->create();

        $this->actingAs($user);

        Livewire::test(UserSupport::class)
            ->assertStatus(200)
            ->assertSee('Open Documentation')
            ->assertSee('GitHub Issues')
            ->assertSee('Paid Support');
    }
}
