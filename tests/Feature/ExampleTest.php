<?php

namespace Tests\Feature;

use Illuminate\Foundation\Testing\RefreshDatabase;
use Tests\TestCase;

class ExampleTest extends TestCase
{
    use RefreshDatabase;

    /**
     * Home page redirects to login (since Filament handles auth).
     */
    public function test_home_redirects_to_login(): void
    {
        $response = $this->get('/');

        $response->assertStatus(302);
    }

    /**
     * User panel login page loads.
     */
    public function test_user_panel_login_page_loads(): void
    {
        $response = $this->get('/jabali-panel/login');

        $response->assertStatus(200);
    }

    /**
     * Admin panel login page loads.
     */
    public function test_admin_panel_login_page_loads(): void
    {
        $response = $this->get('/jabali-admin/login');

        $response->assertStatus(200);
    }
}
