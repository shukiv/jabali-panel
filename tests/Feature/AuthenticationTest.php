<?php

namespace Tests\Feature;

use App\Filament\Jabali\Pages\Auth\Login;
use App\Models\User;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Livewire\Livewire;
use Tests\TestCase;

class AuthenticationTest extends TestCase
{
    use RefreshDatabase;

    public function test_user_panel_login_screen_can_be_rendered(): void
    {
        $response = $this->get('/jabali-panel/login');

        $response->assertStatus(200);
    }

    public function test_admin_panel_login_screen_can_be_rendered(): void
    {
        $response = $this->get('/jabali-admin/login');

        $response->assertStatus(200);
    }

    public function test_users_can_authenticate_via_user_panel(): void
    {
        $user = User::factory()->create();

        Livewire::test(Login::class)
            ->set('data.email', $user->email)
            ->set('data.password', 'password')
            ->call('authenticate')
            ->assertHasNoFormErrors()
            ->assertRedirect('/jabali-panel');
    }

    public function test_users_cannot_authenticate_with_invalid_password(): void
    {
        $user = User::factory()->create();

        Livewire::test(Login::class)
            ->set('data.email', $user->email)
            ->set('data.password', 'wrong-password')
            ->call('authenticate')
            ->assertHasFormErrors(['email']);
    }

    public function test_inactive_user_cannot_login(): void
    {
        $user = User::factory()->inactive()->create();

        Livewire::test(Login::class)
            ->set('data.email', $user->email)
            ->set('data.password', 'password')
            ->call('authenticate')
            ->assertHasFormErrors(['email']);
    }
}
